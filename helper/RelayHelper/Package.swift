// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "RelayHelper",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "RelayHelper", targets: ["RelayHelper"])
    ],
    targets: [
        .executableTarget(
            name: "RelayHelper",
            path: "Sources/RelayHelper",
            swiftSettings: [
                .define("MACOS_APPKIT")
            ]
        ),
        .testTarget(
            name: "RelayHelperTests",
            dependencies: ["RelayHelper"],
            path: "Tests/RelayHelperTests"
        )
    ]
)
