// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "Shim",
    platforms: [
        .macOS(.v12),
    ],
    targets: [
        .executableTarget(
            name: "Shim",
            path: "Sources/Shim"
        ),
    ]
)
