import Foundation
import Observation
import YccProto

/// The narrow data source a ``HealthModel`` reads from (`GetHealth`).
public protocol HealthSource: Sendable {
    func getHealth(refresh: Bool) async throws -> [Ycc_V1_HealthCheck]
}

extension YccClient: HealthSource {
    /// The daemon's latest dependency health checks. `refresh` re-runs every
    /// check first, which can take most of a minute, so it gets the bulk
    /// deadline.
    public func getHealth(refresh: Bool) async throws -> [Ycc_V1_HealthCheck] {
        var request = Ycc_V1_GetHealthRequest()
        request.refresh = refresh
        let service = refresh ? generatedBulk : generated
        let response = await service.getHealth(request: request)
        switch response.result {
        case .success(let message): return message.checks
        case .failure(let error): throw Self.map(error)
        }
    }
}

/// The daemon's dependency health (models, Exa, disk) for the home banner and
/// the Health sheet. Health is supplementary: a failed passive load (an older
/// daemon without `GetHealth`, a dropped connection) keeps the last snapshot
/// and surfaces nothing; only an explicit **Check now** reports its failure.
@MainActor
@Observable
public final class HealthModel {
    public enum Severity: Equatable, Sendable {
        case warn, fail
    }

    public private(set) var checks: [Ycc_V1_HealthCheck] = []
    /// A user-initiated re-check is in flight.
    public private(set) var isChecking = false
    /// Why the last **Check now** failed; cleared by the next success.
    public private(set) var checkError: String?

    private let source: HealthSource
    /// Request ordering: a response older than the last applied one is
    /// dropped, so a slow passive read can't replace a fresher re-check.
    private var issued = 0
    private var applied = 0

    public init(source: HealthSource) {
        self.source = source
    }

    /// Read the latest results (no provider requests). Skipped while a
    /// re-check is running — that call returns fresher results anyway.
    public func refresh() async {
        guard !isChecking else { return }
        _ = await load(refresh: false)
    }

    /// Re-run every check on the daemon, then show the results.
    public func checkNow() async {
        guard !isChecking else { return }
        isChecking = true
        defer { isChecking = false }
        if let error = await load(refresh: true) {
            checkError = error
        }
    }

    /// Returns a failure message, or nil on success.
    private func load(refresh: Bool) async -> String? {
        issued += 1
        let id = issued
        do {
            let loaded = try await source.getHealth(refresh: refresh)
            guard id > applied else { return nil }
            applied = id
            if loaded != checks { checks = loaded }
            checkError = nil
            return nil
        } catch let YccError.rpc(message) {
            return message
        } catch {
            return error.localizedDescription
        }
    }

    /// The checks that need attention, failures first, else in daemon order.
    public var alerting: [Ycc_V1_HealthCheck] {
        Self.alerting(checks)
    }

    public static func alerting(_ checks: [Ycc_V1_HealthCheck]) -> [Ycc_V1_HealthCheck] {
        checks.filter { severity(of: $0) == .fail } + checks.filter { severity(of: $0) == .warn }
    }

    /// `warn`/`fail` alert; `pending`, `ok`, `off`, and unknown values don't.
    public static func severity(of check: Ycc_V1_HealthCheck) -> Severity? {
        switch check.status {
        case "fail": return .fail
        case "warn": return .warn
        default: return nil
        }
    }

    /// When the check last ran; nil while pending (an empty `checked_at`).
    public static func checkedAt(_ check: Ycc_V1_HealthCheck) -> Date? {
        SessionListModel.parseTimestamp(check.checkedAt)
    }

    public static func label(_ check: Ycc_V1_HealthCheck) -> String {
        check.label.isEmpty ? check.id : check.label
    }

    /// The banner's "<label> <word>" item, worded like the web client's.
    public static func bannerItem(_ check: Ycc_V1_HealthCheck) -> String {
        let word = check.id.hasPrefix("disk:") ? "low" : check.status == "fail" ? "failing" : "degraded"
        return "\(label(check)) \(word)"
    }
}
