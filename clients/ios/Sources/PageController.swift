import Foundation
import WebKit

/// The window: the same page as the desktop app, in a web view. It also gives
/// the page the few things only the app can do (see the script below).
final class PageController: NSObject, ObservableObject, WKNavigationDelegate, WKScriptMessageHandler {
    @Published var showScanner = false
    let webView: WKWebView
    private var loaded: URL?

    // What the page may ask the app for. window.ephdropPhone is the same name the Android app uses.
    private static let script = """
    window.ephdropPhone = {
      scanInvite: function () { window.webkit.messageHandlers.ephdrop.postMessage({ op: "scan" }); },
      exportFile: function (path, name) { window.webkit.messageHandlers.ephdrop.postMessage({ op: "saved", name: name }); }
    };
    """

    override init() {
        let config = WKWebViewConfiguration()
        let controller = WKUserContentController()
        controller.addUserScript(WKUserScript(source: Self.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        config.userContentController = controller
        webView = WKWebView(frame: .zero, configuration: config)
        super.init()
        controller.add(self, name: "ephdrop")
        webView.navigationDelegate = self
        webView.isOpaque = false
        webView.backgroundColor = .white
        webView.scrollView.backgroundColor = .white
        webView.allowsBackForwardNavigationGestures = false
    }

    /// Opens the page. Each start of the program has a new address and secret.
    func load(_ url: URL) {
        guard loaded != url else { return }
        loaded = url
        webView.load(URLRequest(url: url))
    }

    /// Called with the text of a scanned code.
    func inviteScanned(_ text: String) {
        evaluate("window.onInviteScanned(\(Self.quote(text)))")
    }

    // The window only ever shows the page served by our own program.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        decisionHandler(action.request.url?.host == "127.0.0.1" ? .allow : .cancel)
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.request.url?.host == "127.0.0.1",
              let body = message.body as? [String: Any], let op = body["op"] as? String else { return }
        switch op {
        case "scan":
            DispatchQueue.main.async { self.showScanner = true }
        case "saved":
            // Fetched files are already in the app's Documents folder, which the Files app shows.
            let name = (body["name"] as? String) ?? "File"
            evaluate("toast(\(Self.quote("Saved \(name). Find it in Files, under On My iPhone, ephdrop.")))")
        default:
            break
        }
    }

    private func evaluate(_ js: String) {
        DispatchQueue.main.async { self.webView.evaluateJavaScript(js, completionHandler: nil) }
    }

    /// A string safe to put inside JavaScript.
    private static func quote(_ s: String) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: [s]),
              let text = String(data: data, encoding: .utf8) else { return "\"\"" }
        return String(text.dropFirst().dropLast()) // strip the [ ]
    }
}
