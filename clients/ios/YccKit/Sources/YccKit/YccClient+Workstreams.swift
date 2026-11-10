import YccProto

extension YccClient {
    /// Start a session in a new workstream (`SpawnWorkstream`): its own git
    /// worktree and branch off the project's base. The session takes the same
    /// choices as `StartSession` (mode, coordinator override, preset, opening
    /// pictures) plus an optional backlog task. The session id to subscribe to
    /// rides in the returned ``Ycc_V1_WorkstreamInfo``.
    public func spawnWorkstream(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        preset: String, taskID: String, images: [MessageImage]
    ) async throws -> Ycc_V1_WorkstreamInfo {
        var request = Ycc_V1_SpawnWorkstreamRequest()
        request.project = project
        request.mode = mode
        request.prompt = prompt
        request.coordinatorModel = coordinatorModel
        request.preset = preset
        request.taskID = taskID
        request.images = images.map { image in
            var attachment = Ycc_V1_ImageAttachment()
            attachment.data = image.data
            attachment.mediaType = image.mediaType
            attachment.filename = image.filename
            return attachment
        }
        // Creating the worktree and branch takes longer than an ordinary call.
        let response = await generatedBulk.spawnWorkstream(request: request)
        switch response.result {
        case .success(let message):
            return message.workstream
        case .failure(let error):
            throw Self.map(error)
        }
    }
}
