import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``AddProjectSource`` for headless model tests. Records
/// the last submitted arguments so the request round-trip is testable.
private final class MockAddProjectSource: AddProjectSource, @unchecked Sendable {
    var error: Error?
    var returnedName = "demo"

    private(set) var addArgs: (path: String, name: String)?

    func addProject(path: String, name: String) async throws -> Ycc_V1_ProjectInfo {
        addArgs = (path, name)
        if let error { throw error }
        var p = Ycc_V1_ProjectInfo()
        p.name = returnedName.isEmpty ? (path as NSString).lastPathComponent : returnedName
        p.path = path
        return p
    }
}

@MainActor
final class AddProjectModelTests: XCTestCase {
    // MARK: - Validation

    func testPathValidation() {
        XCTAssertFalse(AddProjectModel.isPlausiblePath(""))
        XCTAssertFalse(AddProjectModel.isPlausiblePath("   "))
        XCTAssertFalse(AddProjectModel.isPlausiblePath("relative/path"))
        XCTAssertFalse(AddProjectModel.isPlausiblePath("~/code/ycc"))
        XCTAssertFalse(AddProjectModel.isPlausiblePath("/"))
        XCTAssertTrue(AddProjectModel.isPlausiblePath("/home/me/code/ycc"))
        XCTAssertTrue(AddProjectModel.isPlausiblePath("  /home/me/code/ycc \n"))
    }

    func testCanSubmitTracksPathValidity() {
        let model = AddProjectModel(source: MockAddProjectSource())
        XCTAssertFalse(model.canSubmit)
        model.path = "nope"
        XCTAssertFalse(model.canSubmit)
        model.path = "/srv/repo"
        XCTAssertTrue(model.canSubmit)
    }

    // MARK: - Submit

    func testSubmitTrimsAndReturnsProject() async {
        let source = MockAddProjectSource()
        let model = AddProjectModel(source: source)
        model.path = "  /home/me/code/ycc  "
        model.name = " ycc \n"

        let project = await model.submit()

        XCTAssertEqual(project?.name, "demo")
        XCTAssertEqual(source.addArgs?.path, "/home/me/code/ycc")
        XCTAssertEqual(source.addArgs?.name, "ycc")
        XCTAssertNil(model.errorMessage)
        XCTAssertFalse(model.unauthorized)
    }

    func testSubmitWithInvalidPathIsANoop() async {
        let source = MockAddProjectSource()
        let model = AddProjectModel(source: source)
        model.path = "relative"

        let project = await model.submit()

        XCTAssertNil(project)
        XCTAssertNil(source.addArgs)
    }

    func testSubmitSurfacesRPCError() async {
        let source = MockAddProjectSource()
        source.error = YccError.rpc(message: "no such directory")
        let model = AddProjectModel(source: source)
        model.path = "/nope"

        let project = await model.submit()

        XCTAssertNil(project)
        XCTAssertEqual(model.errorMessage, "no such directory")
        XCTAssertFalse(model.unauthorized)
    }

    func testSubmitUnauthorizedSetsFlag() async {
        let source = MockAddProjectSource()
        source.error = YccError.unauthorized
        let model = AddProjectModel(source: source)
        model.path = "/srv/repo"

        let project = await model.submit()

        XCTAssertNil(project)
        XCTAssertTrue(model.unauthorized)
        XCTAssertNil(model.errorMessage)
    }

    func testErrorClearsOnNextSuccess() async {
        let source = MockAddProjectSource()
        source.error = YccError.rpc(message: "boom")
        let model = AddProjectModel(source: source)
        model.path = "/srv/repo"

        _ = await model.submit()
        XCTAssertNotNil(model.errorMessage)

        source.error = nil
        let project = await model.submit()
        XCTAssertNotNil(project)
        XCTAssertNil(model.errorMessage)
    }
}
