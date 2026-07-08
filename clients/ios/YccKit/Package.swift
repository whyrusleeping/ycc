// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "YccKit",
    platforms: [
        .iOS(.v17),
        // macOS is supported so the non-UI logic (client request shaping,
        // connection store) runs under `swift test` headlessly on the workspace
        // machine — no simulator required. See docs/design/ios-client.md §3/§10.
        .macOS(.v14),
    ],
    products: [
        .library(name: "YccKit", targets: ["YccKit"]),
    ],
    dependencies: [
        .package(url: "https://github.com/connectrpc/connect-swift.git", from: "1.0.0"),
    ],
    targets: [
        // Generated Swift protos + connect-swift service client (committed;
        // regenerated via buf.gen.swift.yaml — do not hand-edit).
        .target(
            name: "YccProto",
            dependencies: [
                .product(name: "Connect", package: "connect-swift"),
            ],
            path: "Sources/YccProto"
        ),
        // All non-UI logic: the YccClient wrapper + ConnectionStore.
        .target(
            name: "YccKit",
            dependencies: [
                "YccProto",
                .product(name: "Connect", package: "connect-swift"),
            ],
            path: "Sources/YccKit"
        ),
        .testTarget(
            name: "YccKitTests",
            dependencies: ["YccKit"],
            path: "Tests/YccKitTests"
        ),
    ]
)
