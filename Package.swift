// swift-tools-version: 6.4
import PackageDescription

let package = Package(
    name: "Tandem",
    platforms: [.iOS(.v27), .macOS(.v27)],
    products: [
        .library(name: "Tandem", targets: ["Tandem"]),
        .library(name: "TandemIMAP", targets: ["TandemIMAP"]),
        .library(name: "TandemEngine", targets: ["TandemEngine"]),
    ],
    targets: [
        .target(name: "Tandem", path: "sdk/swift/Sources/Tandem"),
        .target(name: "TandemIMAP", dependencies: ["Tandem"], path: "sdk/swift/Sources/TandemIMAP"),
        .target(name: "TandemEngine", dependencies: ["Tandem"], path: "sdk/swift/Sources/TandemEngine"),
        .testTarget(name: "TandemTests", dependencies: ["Tandem"], path: "sdk/swift/Tests/TandemTests"),
        .testTarget(name: "TandemIMAPTests", dependencies: ["TandemIMAP", "TandemEngine"], path: "sdk/swift/Tests/TandemIMAPTests"),
        .testTarget(name: "TandemEngineTests", dependencies: ["Tandem", "TandemEngine"],
                    path: "sdk/swift/Tests/TandemEngineTests"),
    ]
)
