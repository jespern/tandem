// swift-tools-version: 6.4
// A test helper for sdk/go/imap (TestSwiftInterop): Swift's MessageFormat builds and reads
// §10.4 messages for the Go binding to compare against. Not part of any product.
import PackageDescription

let package = Package(
    name: "swiftinterop",
    platforms: [.macOS(.v27)],
    dependencies: [.package(name: "Tandem", path: "../../../../..")],
    targets: [
        .executableTarget(name: "swiftinterop", dependencies: [.product(name: "TandemIMAP", package: "Tandem")]),
    ]
)
