import Foundation
import Observation
import YccProto

/// The data source ``ReviewTiersModel`` reads from and drives. Abstracted behind
/// a protocol so the logic is unit-testable
/// headlessly with an in-memory mock; ``YccClient`` is the production conformer.
public protocol ReviewTiersSource: Sendable {
    /// Effective tiers (built-ins overlaid with configured) + default tier name.
    func listReviewTiers() async throws -> Ycc_V1_ListReviewTiersResponse
    /// Configured logical models (for the per-slot model pickers).
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    func upsertReviewTier(_ tier: Ycc_V1_ReviewTierInfo) async throws
    func removeReviewTier(name: String) async throws
    func setReviewDefault(name: String) async throws
}

extension YccClient: ReviewTiersSource {}

/// A review tier's strategy. `agents` spawns reviewer subagents;
/// `selfReview` means the coordinator reviews the change itself.
public enum ReviewStrategy: String, CaseIterable, Sendable, Identifiable {
    case agents
    case selfReview

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .agents: return "Reviewer agents"
        case .selfReview: return "Coordinator self-review"
        }
    }

    /// The value stored in the tier's `strategy` field (`""` means agents).
    public var wireValue: String { self == .selfReview ? "coordinator" : "" }

    /// Parse a config strategy string ("", "agents", "coordinator", "self",
    /// "self-review").
    public static func parse(_ value: String) -> ReviewStrategy {
        switch value {
        case "coordinator", "self", "self-review": return .selfReview
        default: return .agents
        }
    }
}

/// One editable reviewer slot of a tier draft: which model reviews, under which
/// label, with what focus and optional per-reviewer thinking level (`nil` =
/// inherit the reviewers role level / model default).
public struct ReviewerSlotDraft: Identifiable, Equatable, Sendable {
    public let id: UUID
    public var name: String
    public var model: String
    public var prompt: String
    public var thinking: ThinkingLevel?

    public init(
        id: UUID = UUID(), name: String = "", model: String = "",
        prompt: String = "", thinking: ThinkingLevel? = nil
    ) {
        self.id = id
        self.name = name
        self.model = model
        self.prompt = prompt
        self.thinking = thinking
    }

    /// True when the slot carries nothing beyond the model — used to decide
    /// whether the whole tier can round-trip as the `models` shorthand.
    public var isGeneric: Bool {
        name.trimmingCharacters(in: .whitespaces).isEmpty
            && prompt.trimmingCharacters(in: .whitespaces).isEmpty
            && thinking == nil
    }
}

/// An editable review tier. Seeded from a wire tier (expanding the `models`
/// shorthand into slots) and serialized back, restoring the shorthand when every
/// slot is generic so simple tiers keep their compact ycc.toml shape.
public struct ReviewTierDraft: Equatable, Sendable {
    public var name: String
    public var strategy: ReviewStrategy
    public var description: String
    public var prompt: String
    public var slots: [ReviewerSlotDraft]
    /// True when editing an existing tier (the name is immutable then — renaming
    /// is remove + add).
    public var isExisting: Bool
    /// True when this name is one of the always-present built-ins.
    public var isBuiltin: Bool

    public init(
        name: String = "", strategy: ReviewStrategy = .agents, description: String = "",
        prompt: String = "", slots: [ReviewerSlotDraft] = [],
        isExisting: Bool = false, isBuiltin: Bool = false
    ) {
        self.name = name
        self.strategy = strategy
        self.description = description
        self.prompt = prompt
        self.slots = slots
        self.isExisting = isExisting
        self.isBuiltin = isBuiltin
    }

    /// Seed a draft from a wire tier, expanding the models shorthand into
    /// generic slots so the editor always works with slots.
    public init(tier: Ycc_V1_ReviewTierInfo) {
        name = tier.name
        strategy = ReviewStrategy.parse(tier.strategy)
        description = tier.description_p
        prompt = tier.prompt
        if tier.reviewers.isEmpty {
            slots = tier.models.map { ReviewerSlotDraft(model: $0) }
        } else {
            slots = tier.reviewers.map { slot in
                ReviewerSlotDraft(
                    name: slot.name, model: slot.model, prompt: slot.prompt,
                    thinking: slot.thinking.isEmpty ? nil : ThinkingLevel.parse(slot.thinking))
            }
        }
        isExisting = true
        isBuiltin = tier.builtin
    }

    /// Serialize back to the wire shape. Slots with an empty model are dropped;
    /// when every remaining slot is generic the compact `models` shorthand is
    /// emitted instead of the long form.
    public func toProto() -> Ycc_V1_ReviewTierInfo {
        var tier = Ycc_V1_ReviewTierInfo()
        tier.name = name.trimmingCharacters(in: .whitespaces)
        tier.strategy = strategy.wireValue
        tier.description_p = description.trimmingCharacters(in: .whitespaces)
        tier.prompt = prompt.trimmingCharacters(in: .whitespaces)
        let kept = slots.filter { !$0.model.trimmingCharacters(in: .whitespaces).isEmpty }
        if strategy == .selfReview {
            return tier // models/reviewers are ignored for self-review tiers
        }
        if kept.allSatisfy(\.isGeneric) {
            tier.models = kept.map { $0.model.trimmingCharacters(in: .whitespaces) }
        } else {
            tier.reviewers = kept.map { slot in
                var out = Ycc_V1_ReviewerSlot()
                out.name = slot.name.trimmingCharacters(in: .whitespaces)
                out.model = slot.model.trimmingCharacters(in: .whitespaces)
                out.prompt = slot.prompt.trimmingCharacters(in: .whitespaces)
                out.thinking = slot.thinking?.wireValue ?? ""
                return out
            }
        }
        return tier
    }

    /// Client-side validity: a name, and (for agents tiers) at least one slot
    /// with a model. The daemon remains the authority; this only gates the Save
    /// button on obviously incomplete drafts.
    public var isSavable: Bool {
        guard !name.trimmingCharacters(in: .whitespaces).isEmpty else { return false }
        if strategy == .selfReview { return true }
        return slots.contains { !$0.model.trimmingCharacters(in: .whitespaces).isEmpty }
    }
}

/// Drives the review-tiers screen of the iOS global settings: lists the effective
/// tiers, sets the default, and saves/removes
/// configured tiers via the injected ``ReviewTiersSource``. Daemon errors
/// surface verbatim. `@MainActor` because it publishes observable UI state.
@MainActor
@Observable
public final class ReviewTiersModel {
    /// The effective tiers, as reported by the daemon (sorted by name).
    public private(set) var tiers: [Ycc_V1_ReviewTierInfo] = []
    /// The effective default tier name.
    public private(set) var defaultTier = ""
    /// Configured logical model names (for the per-slot model pickers).
    public private(set) var modelNames: [String] = []

    public private(set) var isLoading = false
    public private(set) var isApplying = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: ReviewTiersSource

    public init(source: ReviewTiersSource) {
        self.source = source
    }

    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listReviewTiers()
            let models = try await source.listModels()
            tiers = response.tiers
            defaultTier = response.defaultTier
            modelNames = models.models.map(\.name)
                .sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
            errorMessage = nil
        } catch { handle(error) }
    }

    /// Set `reviews.default`. On failure the previous default is restored.
    public func setDefault(_ name: String) async {
        guard name != defaultTier else { return }
        let previous = defaultTier
        defaultTier = name
        await apply {
            try await self.source.setReviewDefault(name: name)
        } onFailure: {
            self.defaultTier = previous
        }
    }

    /// Save a draft (upsert) and reload so derived flags/ordering reflect the
    /// daemon's view. Returns true on success so the editor can dismiss.
    public func save(_ draft: ReviewTierDraft) async -> Bool {
        var ok = false
        await apply {
            try await self.source.upsertReviewTier(draft.toProto())
            ok = true
        } onFailure: {}
        if ok { await load() }
        return ok
    }

    /// Remove a configured tier entry (a built-in reverts to built-in
    /// behaviour). Returns true on success.
    public func remove(name: String) async -> Bool {
        var ok = false
        await apply {
            try await self.source.removeReviewTier(name: name)
            ok = true
        } onFailure: {}
        if ok { await load() }
        return ok
    }

    /// Whether the tier row should offer removal: only tiers with a configured
    /// entry have anything to remove, and the default custom tier is refused by
    /// the daemon (change the default first) — mirror that so the affordance
    /// matches what would succeed.
    public func isRemovable(_ tier: Ycc_V1_ReviewTierInfo) -> Bool {
        guard tier.configured else { return false }
        if !tier.builtin && tier.name == defaultTier { return false }
        return true
    }

    public func clearError() { errorMessage = nil }

    // MARK: - Apply plumbing

    private func apply(
        _ body: @escaping () async throws -> Void,
        onFailure: @escaping () -> Void
    ) async {
        guard !isApplying else {
            onFailure()
            return
        }
        isApplying = true
        defer { isApplying = false }
        do {
            try await body()
            errorMessage = nil
        } catch {
            onFailure()
            handle(error)
        }
    }

    private func handle(_ error: Error) {
        switch error {
        case YccError.unauthorized: unauthorized = true
        case let YccError.rpc(message): errorMessage = message
        case let YccError.notFound(message): errorMessage = message
        case let YccError.failedPrecondition(message): errorMessage = message
        default: errorMessage = error.localizedDescription
        }
    }
}
