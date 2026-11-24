import YccProto

extension YccClient {
    public func startSession(_ request: Ycc_V1_StartSessionRequest) async throws -> String {
        let response = await generatedBulk.startSession(request: request)
        switch response.result {
        case .success(let message): return message.sessionID
        case .failure(let error): throw Self.map(error)
        }
    }
}

extension Ycc_V1_StartSessionRequest {
    init(project: String, mode: String, prompt: String, coordinatorModel: String,
         coordinatorThinking: String, images: [MessageImage], subagentModels: Set<String>?) {
        self.init()
        self.project = project
        self.mode = mode
        self.prompt = prompt
        self.coordinatorModel = coordinatorModel
        self.coordinatorThinking = coordinatorThinking
        self.images = images.map { image in
            .with {
                $0.data = image.data
                $0.mediaType = image.mediaType
                $0.filename = image.filename
            }
        }
        if let subagentModels {
            self.subagentModels = .with { $0.names = subagentModels.sorted() }
        }
    }
}

extension Ycc_V1_SpawnWorkstreamRequest {
    init(opening: Ycc_V1_StartSessionRequest, preset: String, taskID: String) {
        self.init()
        self.project = opening.project
        self.mode = opening.mode
        self.prompt = opening.prompt
        self.coordinatorModel = opening.coordinatorModel
        self.coordinatorThinking = opening.coordinatorThinking
        self.images = opening.images
        if opening.hasSubagentModels { self.subagentModels = opening.subagentModels }
        self.preset = preset
        self.taskID = taskID
    }
}
