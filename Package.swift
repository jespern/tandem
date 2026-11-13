// swift-tools-version: 6.4
import PackageDescription

let package = Package(
    name: "Tandem",
    platforms: [.iOS(.v27), .macOS(.v27)],
    products: [.library(name: "Tandem", targets: ["Tandem"])],
    targets: [
        .target(name: "Tandem", path: "sdk/swift/Sources/Tandem"),
        .testTarget(name: "TandemTests", dependencies: ["Tandem"], path: "sdk/swift/Tests/TandemTests"),
    ]
)
