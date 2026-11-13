// swift-tools-version: 6.4
import PackageDescription

let package = Package(
    name: "Tandem",
    platforms: [.iOS(.v27), .macOS(.v27)],
    products: [
        .library(name: "Tandem", targets: ["Tandem"]),
        .library(name: "TandemIMAP", targets: ["TandemIMAP"]),
    ],
    targets: [
        .target(name: "Tandem", path: "sdk/swift/Sources/Tandem"),
        .target(name: "TandemIMAP", dependencies: ["Tandem"], path: "sdk/swift/Sources/TandemIMAP"),
        .testTarget(name: "TandemTests", dependencies: ["Tandem"], path: "sdk/swift/Tests/TandemTests"),
        .testTarget(name: "TandemIMAPTests", dependencies: ["TandemIMAP"], path: "sdk/swift/Tests/TandemIMAPTests"),
    ]
)
