import SwiftUI
import WebKit

struct PageView: UIViewRepresentable {
    let controller: PageController
    func makeUIView(context: Context) -> WKWebView { controller.webView }
    func updateUIView(_ uiView: WKWebView, context: Context) {}
}

struct ContentView: View {
    @EnvironmentObject private var engine: Engine
    @StateObject private var page = PageController()

    var body: some View {
        ZStack {
            Color.white.ignoresSafeArea()
            PageView(controller: page)
            if let message = engine.error {
                VStack(spacing: 8) {
                    Text("ephdrop could not start").font(.headline)
                    Text(message).font(.footnote).multilineTextAlignment(.center)
                    Button("Try again") { engine.start() }
                }
                .padding(24)
                .background(Color.white)
            }
        }
        .onReceive(engine.$pageURL) { url in
            if let url { page.load(url) }
        }
        .sheet(isPresented: $page.showScanner) {
            ScannerView(
                onCode: { code in
                    page.showScanner = false
                    page.inviteScanned(code)
                },
                onCancel: { page.showScanner = false }
            )
            .ignoresSafeArea()
        }
    }
}
