import SwiftUI

@main
struct EphdropApp: App {
    @StateObject private var engine = Engine()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(engine)
                // "Share" or "Open in" from another app hands us a file
                .onOpenURL { engine.share(fileURL: $0) }
        }
        .onChange(of: phase) { newPhase in
            // iPhones do not let apps keep sharing in the background, so ephdrop
            // runs while it is open and stops when it is not.
            switch newPhase {
            case .active: engine.start()
            case .background: engine.stop()
            default: break
            }
        }
    }
}
