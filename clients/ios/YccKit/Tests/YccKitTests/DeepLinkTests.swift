import XCTest
@testable import YccKit

final class DeepLinkTests: XCTestCase {
    private func parse(_ string: String) -> DeepLink? {
        guard let url = URL(string: string) else { return nil }
        return DeepLink(url: url)
    }

    func testSessionHostForm() {
        XCTAssertEqual(parse("ycc://session/abc123"), .session(id: "abc123", server: nil))
    }

    func testSessionWithServerQuery() {
        XCTAssertEqual(
            parse("ycc://session/abc123?server=home"),
            .session(id: "abc123", server: "home"))
    }

    func testSessionEmptyServerQueryIsNil() {
        XCTAssertEqual(parse("ycc://session/abc123?server="), .session(id: "abc123", server: nil))
    }

    func testProjectLink() {
        XCTAssertEqual(parse("ycc://project/myrepo"), .project(name: "myrepo"))
    }

    func testSchemeIsCaseInsensitive() {
        XCTAssertEqual(parse("YCC://session/x"), .session(id: "x", server: nil))
    }

    func testWrongSchemeIsNil() {
        XCTAssertNil(parse("https://session/abc"))
        XCTAssertNil(parse("myapp://session/abc"))
    }

    func testUnknownKindIsNil() {
        XCTAssertNil(parse("ycc://frobnicate/abc"))
    }

    func testMissingSessionIdIsNil() {
        XCTAssertNil(parse("ycc://session"))
        XCTAssertNil(parse("ycc://session/"))
    }

    func testMissingProjectNameIsNil() {
        XCTAssertNil(parse("ycc://project"))
    }

    func testExtraPathSegmentsIgnored() {
        XCTAssertEqual(parse("ycc://session/abc/extra"), .session(id: "abc", server: nil))
    }
}
