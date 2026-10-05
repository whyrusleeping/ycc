import Foundation
import XCTest
import YccProto
@testable import YccKit

final class EventContractTests: XCTestCase {
    private struct Fixture: Decodable {
        let version: Int
        let name: String
        let steps: [Step]
    }
    private struct Step: Decodable {
        let event: ContractEvent?
        let reconnect: [String: String]?
        let expect: [String: JSONValue]?
    }
    private struct ContractEvent: Decodable {
        let seq: Int64
        let actor: String
        let type: String
        let transient: Bool?
        let ts: String?
        let data: [String: JSONValue]
    }
    // Preserve arbitrary synthetic JSON payloads without tying the fixture to a UI model.
    private enum JSONValue: Codable, Equatable {
        case string(String), number(Int), bool(Bool), array([JSONValue]), object([String: JSONValue]), null
        init(from decoder: Decoder) throws {
            let c = try decoder.singleValueContainer()
            if c.decodeNil() { self = .null }
            else if let v = try? c.decode(Bool.self) { self = .bool(v) }
            else if let v = try? c.decode(Int.self) { self = .number(v) }
            else if let v = try? c.decode(String.self) { self = .string(v) }
            else if let v = try? c.decode([JSONValue].self) { self = .array(v) }
            else { self = .object(try c.decode([String: JSONValue].self)) }
        }
        func encode(to encoder: Encoder) throws {
            var c = encoder.singleValueContainer()
            switch self {
            case .null: try c.encodeNil()
            case .string(let v): try c.encode(v)
            case .number(let v): try c.encode(v)
            case .bool(let v): try c.encode(v)
            case .array(let v): try c.encode(v)
            case .object(let v): try c.encode(v)
            }
        }
    }
    private func obj(_ pairs: [String: JSONValue]) -> JSONValue { .object(pairs) }
    private func text(_ value: JSONValue?) -> String { if case .string(let s) = value { return s }; return "" }

    func testSharedEventContract() throws {
        let source = URL(fileURLWithPath: #filePath)
        let directory = ProcessInfo.processInfo.environment["YCC_EVENT_CONTRACT_DIR"].map {
            URL(fileURLWithPath: $0)
        } ?? source.deletingLastPathComponent()
            .appendingPathComponent("../../../../../testdata/event-contract").standardizedFileURL
        let files = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "json" }.sorted { $0.lastPathComponent < $1.lastPathComponent }
        XCTAssertFalse(files.isEmpty)
        for file in files {
            let fixture = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: file))
            XCTAssertEqual(fixture.version, 1, fixture.name)
            var projection = SessionProjection()
            var applied: [ContractEvent] = []
            for (index, step) in fixture.steps.enumerated() {
                if let event = step.event {
                    var wire = Ycc_V1_Event()
                    wire.seq = event.seq
                    wire.actor = event.actor
                    wire.type = event.type
                    wire.transient = event.transient ?? false
                    wire.ts = event.ts ?? ""
                    wire.dataJson = String(data: try JSONEncoder().encode(event.data), encoding: .utf8)!
                    let before = projection.lastPersistedSeq
                    projection.apply(wire)
                    if !wire.transient && wire.seq > before { applied.append(event) }
                } else if step.reconnect != nil {
                    projection.clearLiveTails()
                } else if let expected = step.expect {
                    let facts = facts(projection, events: applied)
                    for (key, value) in expected {
                        guard let actual = facts[key] else { XCTFail("Unknown fact \(key)"); continue }
                        XCTAssertEqual(actual, value, "\(fixture.name) step \(index): \(key)")
                    }
                } else { XCTFail("Empty step \(fixture.name) #\(index)") }
            }
        }
    }

    private func facts(_ projection: SessionProjection, events: [ContractEvent]) -> [String: JSONValue] {
        let phase: String
        switch projection.phase {
        case .running: phase = "running"
        case .paused: phase = "paused"
        case .idle: phase = "idle"
        case .error: phase = "error"
        case .stopped: phase = "stopped"
        }
        let pending: JSONValue = projection.pendingQuestion.map { question in
            obj(["prompts": .array(question.questions.map { .string($0.prompt) }),
                 "options": .array(question.questions.map { .array($0.options.map { .string($0) }) })])
        } ?? .null
        let inputs: [JSONValue] = projection.durableRows.compactMap { row in
            guard case .userMessage(let text, _) = row.kind else { return nil }
            return obj(["seq": .number(Int(row.seq)), "text": .string(text),
                        "delivery": .string(row.userInputStatus == .queued ? "queued" : "delivered")])
        }
        var answers: [JSONValue] = []
        for (i, ev) in events.enumerated() where ev.type == "question_answered" {
            guard let question = events[..<i].last(where: { $0.actor == ev.actor && $0.type == "question_asked" }) else { continue }
            let row = projection.durableRows.first { $0.seq == question.seq }
            let provenance: String
            if case .assumption? = row?.kind { provenance = "automatic" }
            else { provenance = "human" }
            answers.append(obj(["question_seq": .number(Int(question.seq)), "provenance": .string(provenance)]))
        }
        let reviews: [JSONValue] = projection.durableRows.compactMap { row in
            guard case .review(let summary, _, _) = row.kind else { return nil }
            let verdict = summary.contains("— ACCEPT") ? "accept" : summary.contains("— REVISE") ? "revise" : "unknown"
            return obj(["seq": .number(Int(row.seq)), "verdict": .string(verdict)])
        }
        var tails: [String: JSONValue] = [:]
        for row in projection.liveTails {
            if case .liveTail(let text) = row.kind { tails[row.actor] = .string(text) }
        }
        return ["phase": .string(phase), "awaiting_jobs": .bool(projection.awaitingJobs),
                "pause_requested": .bool(projection.pauseRequested),
                "cursor": .number(Int(projection.lastPersistedSeq)), "pending_question": pending,
                "inputs": .array(inputs), "answers": .array(answers), "reviews": .array(reviews), "tails": obj(tails)]
    }
}
