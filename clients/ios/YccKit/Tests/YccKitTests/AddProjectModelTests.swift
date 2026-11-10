import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``AddProjectSource`` for headless model tests. Records
/// the last submitted arguments so each mode's request is testable.
private final class MockAddProjectSource: AddProjectSource, @unchecked Sendable {
    var error: Error?
    var returnedName = "demo"

    private(set) var addArgs: (path: String, name: String, create: Bool, cloneURL: String)?

    func addProject(
        path: String, name: String, create: Bool, cloneURL: String
    ) async throws -> Ycc_V1_ProjectInfo {
        addArgs = (path, name, create, cloneURL)
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

    func testFolderNameFromRepository() {
        let cases: [(String, String)] = [
            ("owner/repo", "repo"),
            ("  owner/repo  ", "repo"),
            ("https://github.com/o/r.git", "r"),
            ("https://github.com/o/r/", "r"),
            ("https://github.com/o/r.git/", "r"),
            ("git@github.com:o/r", "r"),
            ("git@github.com:r.git", "r"),
            ("ssh://git@host/o/r.git", "r"),
            ("repo", "repo"),
            ("https://github.com/o/repo/tree/main/docs", "repo"),
            ("https://github.com/o/repo?tab=readme#top", "repo"),
            ("", ""),
            ("git@github.com:", ""),
            ("o/..", ""),
        ]
        for (input, expected) in cases {
            XCTAssertEqual(AddProjectModel.folderName(fromRepository: input), expected, input)
        }
    }

    func testFolderNameValidation() {
        XCTAssertTrue(AddProjectModel.isValidFolderName("foo"))
        XCTAssertTrue(AddProjectModel.isValidFolderName(" foo "))
        XCTAssertFalse(AddProjectModel.isValidFolderName(""))
        XCTAssertFalse(AddProjectModel.isValidFolderName("  "))
        XCTAssertFalse(AddProjectModel.isValidFolderName("a/b"))
        XCTAssertFalse(AddProjectModel.isValidFolderName("."))
        XCTAssertFalse(AddProjectModel.isValidFolderName(".."))
    }

    func testCanSubmitPerMode() {
        let model = AddProjectModel(source: MockAddProjectSource())

        model.mode = .newFolder
        model.folderName = "foo"
        XCTAssertFalse(model.canSubmit, "a new folder needs a location")
        model.location = "/home/me/code"
        XCTAssertTrue(model.canSubmit)
        model.folderName = "../foo"
        XCTAssertFalse(model.canSubmit)

        model.mode = .clone
        model.folderName = "foo"
        XCTAssertFalse(model.canSubmit, "a clone needs a repository")
        model.repository = "  "
        XCTAssertFalse(model.canSubmit)
        model.repository = "o/r"
        XCTAssertTrue(model.canSubmit)
    }

    func testCloneFolderNameFollowsRepositoryUntilEdited() {
        let model = AddProjectModel(source: MockAddProjectSource())
        model.mode = .clone
        model.repository = "o/first"
        XCTAssertEqual(model.folderName, "first")
        model.repository = "git@github.com:o/second.git"
        XCTAssertEqual(model.folderName, "second")

        model.folderName = "mine"
        model.repository = "o/third"
        XCTAssertEqual(model.folderName, "mine")
    }

    // MARK: - Submit

    func testNewFolderSubmitCreatesUnderLocation() async {
        let source = MockAddProjectSource()
        let model = AddProjectModel(source: source)
        model.mode = .newFolder
        model.location = "/home/me/code/"
        model.folderName = " foo "
        model.name = "ignored"

        _ = await model.submit()

        XCTAssertEqual(source.addArgs?.path, "/home/me/code/foo")
        XCTAssertEqual(source.addArgs?.name, "")
        XCTAssertEqual(source.addArgs?.create, true)
        XCTAssertEqual(source.addArgs?.cloneURL, "")
    }

    func testNewFolderAtFilesystemRoot() {
        let model = AddProjectModel(source: MockAddProjectSource())
        model.mode = .newFolder
        model.location = "/"
        model.folderName = "foo"
        XCTAssertEqual(model.targetPath, "/foo")
    }

    func testCloneSubmitSendsRepositoryAndTarget() async {
        let source = MockAddProjectSource()
        let model = AddProjectModel(source: source)
        model.mode = .clone
        model.location = "/home/me/code"
        model.repository = " https://github.com/o/r.git "

        _ = await model.submit()

        XCTAssertEqual(source.addArgs?.path, "/home/me/code/r")
        XCTAssertEqual(source.addArgs?.name, "")
        XCTAssertEqual(source.addArgs?.create, false)
        XCTAssertEqual(source.addArgs?.cloneURL, "https://github.com/o/r.git")
    }

    func testSubmitTrimsAndReturnsProject() async {
        let source = MockAddProjectSource()
        let model = AddProjectModel(source: source)
        model.path = "  /home/me/code/ycc  "
        model.name = " ycc \n"

        let project = await model.submit()

        XCTAssertEqual(project?.name, "demo")
        XCTAssertEqual(source.addArgs?.path, "/home/me/code/ycc")
        XCTAssertEqual(source.addArgs?.name, "ycc")
        XCTAssertEqual(source.addArgs?.create, false)
        XCTAssertEqual(source.addArgs?.cloneURL, "")
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
