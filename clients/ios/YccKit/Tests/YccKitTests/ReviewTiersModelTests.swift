import XCTest
import YccProto
@testable import YccKit

private final class MockReviewTiersSource: ReviewTiersSource, @unchecked Sendable {
    var tiersResponse = Ycc_V1_ListReviewTiersResponse()
    var modelsResponse = Ycc_V1_ListModelsResponse()
    var error: Error?
    private(set) var upserted: Ycc_V1_ReviewTierInfo?
    private(set) var removed: String?
    private(set) var defaultSet: String?

    func listReviewTiers() async throws -> Ycc_V1_ListReviewTiersResponse {
        if let error { throw error }
        return tiersResponse
    }
    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        if let error { throw error }
        return modelsResponse
    }
    func upsertReviewTier(_ tier: Ycc_V1_ReviewTierInfo) async throws {
        if let error { throw error }
        upserted = tier
        // Reflect the upsert in subsequent lists so save→reload sees it.
        var updated = tiersResponse.tiers.filter { $0.name != tier.name }
        var stored = tier
        stored.configured = true
        updated.append(stored)
        tiersResponse.tiers = updated.sorted { $0.name < $1.name }
    }
    func removeReviewTier(name: String) async throws {
        if let error { throw error }
        removed = name
        tiersResponse.tiers.removeAll { $0.name == name && !$0.builtin }
    }
    func setReviewDefault(name: String) async throws {
        if let error { throw error }
        defaultSet = name
        tiersResponse.defaultTier = name.isEmpty ? "single-opus" : name
    }
}

private func builtinTier(_ name: String, strategy: String = "") -> Ycc_V1_ReviewTierInfo {
    var tier = Ycc_V1_ReviewTierInfo()
    tier.name = name
    tier.strategy = strategy
    tier.builtin = true
    return tier
}

private func seededSource() -> MockReviewTiersSource {
    let source = MockReviewTiersSource()
    source.tiersResponse.tiers = [
        builtinTier("high-powered"),
        builtinTier("simple", strategy: "coordinator"),
        builtinTier("single-opus"),
    ]
    source.tiersResponse.defaultTier = "single-opus"
    var claude = Ycc_V1_ModelInfo(); claude.name = "claude"
    var gpt = Ycc_V1_ModelInfo(); gpt.name = "gpt"
    source.modelsResponse.models = [gpt, claude]
    return source
}

@MainActor
final class ReviewTiersModelTests: XCTestCase {
    func testSetDefaultAppliesAndRevertsOnFailure() async {
        let source = seededSource()
        let model = ReviewTiersModel(source: source)
        await model.load()

        await model.setDefault("simple")
        XCTAssertEqual(source.defaultSet, "simple")
        XCTAssertEqual(model.defaultTier, "simple")

        source.error = YccError.rpc("unknown review tier \"nope\"")
        await model.setDefault("nope")
        XCTAssertEqual(model.defaultTier, "simple", "failed default change must revert")
        XCTAssertEqual(model.errorMessage, "unknown review tier \"nope\"")
    }

    func testSaveUpsertsAndReloads() async {
        let source = seededSource()
        let model = ReviewTiersModel(source: source)
        await model.load()

        var draft = ReviewTierDraft(name: "deep")
        draft.description = "risky changes"
        draft.slots = [
            ReviewerSlotDraft(name: "perf", model: "gpt", prompt: "Focus on perf.", thinking: .max)
        ]
        let ok = await model.save(draft)

        XCTAssertTrue(ok)
        XCTAssertEqual(source.upserted?.name, "deep")
        XCTAssertEqual(source.upserted?.reviewers.first?.thinking, "max")
        XCTAssertTrue(model.tiers.contains { $0.name == "deep" && $0.configured })
    }

    func testSaveFailureSurfacesDaemonError() async {
        let source = seededSource()
        let model = ReviewTiersModel(source: source)
        await model.load()
        source.error = YccError.rpc("reviews.tiers.bad: unknown model \"missing\"")

        let ok = await model.save(ReviewTierDraft(
            name: "bad", slots: [ReviewerSlotDraft(model: "missing")]))

        XCTAssertFalse(ok)
        XCTAssertEqual(model.errorMessage, "reviews.tiers.bad: unknown model \"missing\"")
    }

    func testRemovableRules() async {
        let source = seededSource()
        var custom = Ycc_V1_ReviewTierInfo()
        custom.name = "deep"
        custom.configured = true
        var overridden = builtinTier("simple", strategy: "coordinator")
        overridden.configured = true
        source.tiersResponse.tiers.append(custom)
        source.tiersResponse.tiers[1] = overridden
        source.tiersResponse.defaultTier = "deep"
        let model = ReviewTiersModel(source: source)
        await model.load()

        // Unconfigured builtins have nothing to remove; configured overrides do.
        XCTAssertFalse(model.isRemovable(model.tiers.first { $0.name == "high-powered" }!))
        XCTAssertTrue(model.isRemovable(model.tiers.first { $0.name == "simple" }!))
        // The default CUSTOM tier is not removable until the default moves.
        XCTAssertFalse(model.isRemovable(model.tiers.first { $0.name == "deep" }!))
        await model.setDefault("single-opus")
        XCTAssertTrue(model.isRemovable(model.tiers.first { $0.name == "deep" }!))

        let ok = await model.remove(name: "deep")
        XCTAssertTrue(ok)
        XCTAssertEqual(source.removed, "deep")
        XCTAssertFalse(model.tiers.contains { $0.name == "deep" })
    }

    func testUnauthorizedFlagged() async {
        let source = seededSource()
        source.error = YccError.unauthorized
        let model = ReviewTiersModel(source: source)
        await model.load()
        XCTAssertTrue(model.unauthorized)
    }
}

final class ReviewTierDraftTests: XCTestCase {
    func testDraftFromModelsShorthandExpandsSlots() {
        var tier = Ycc_V1_ReviewTierInfo()
        tier.name = "standard"
        tier.models = ["claude", "gpt"]
        let draft = ReviewTierDraft(tier: tier)
        XCTAssertEqual(draft.slots.map(\.model), ["claude", "gpt"])
        XCTAssertTrue(draft.slots.allSatisfy(\.isGeneric))
        XCTAssertTrue(draft.isExisting)
    }

    func testGenericSlotsRoundTripAsModelsShorthand() {
        let draft = ReviewTierDraft(name: "standard", slots: [
            ReviewerSlotDraft(model: "claude"),
            ReviewerSlotDraft(model: "gpt"),
            ReviewerSlotDraft(model: "   "), // empty model dropped
        ])
        let proto = draft.toProto()
        XCTAssertEqual(proto.models, ["claude", "gpt"])
        XCTAssertTrue(proto.reviewers.isEmpty, "generic slots must keep the compact shorthand")
    }

    func testFocusedSlotsUseLongForm() {
        let draft = ReviewTierDraft(name: "deep", prompt: "Cite file:line.", slots: [
            ReviewerSlotDraft(name: "perf", model: "gpt", prompt: "Focus on perf.", thinking: .high),
            ReviewerSlotDraft(model: "claude"),
        ])
        let proto = draft.toProto()
        XCTAssertTrue(proto.models.isEmpty)
        XCTAssertEqual(proto.reviewers.count, 2)
        XCTAssertEqual(proto.reviewers[0].name, "perf")
        XCTAssertEqual(proto.reviewers[0].thinking, "high")
        XCTAssertEqual(proto.reviewers[1].thinking, "", "inherit = empty wire value")
        XCTAssertEqual(proto.prompt, "Cite file:line.")
    }

    func testSelfReviewDropsSlots() {
        let draft = ReviewTierDraft(
            name: "simple", strategy: .selfReview,
            slots: [ReviewerSlotDraft(model: "claude")])
        let proto = draft.toProto()
        XCTAssertEqual(proto.strategy, "coordinator")
        XCTAssertTrue(proto.models.isEmpty)
        XCTAssertTrue(proto.reviewers.isEmpty)
    }

    func testSavableRules() {
        XCTAssertFalse(ReviewTierDraft().isSavable, "needs a name")
        XCTAssertFalse(
            ReviewTierDraft(name: "x").isSavable,
            "agents tier needs at least one modeled slot")
        XCTAssertTrue(ReviewTierDraft(name: "x", strategy: .selfReview).isSavable)
        XCTAssertTrue(
            ReviewTierDraft(name: "x", slots: [ReviewerSlotDraft(model: "claude")]).isSavable)
    }

    func testStrategyParsing() {
        XCTAssertEqual(ReviewStrategy.parse(""), .agents)
        XCTAssertEqual(ReviewStrategy.parse("agents"), .agents)
        XCTAssertEqual(ReviewStrategy.parse("coordinator"), .selfReview)
        XCTAssertEqual(ReviewStrategy.parse("self"), .selfReview)
        XCTAssertEqual(ReviewStrategy.parse("self-review"), .selfReview)
    }
}
