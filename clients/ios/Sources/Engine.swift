import Foundation
import UIKit
import Mobile

/// Runs the Go program (core/mobile) and the device finding that goes with it.
final class Engine: ObservableObject {
    @Published var pageURL: URL?
    @Published var error: String?

    private let queue = DispatchQueue(label: "ephdrop.engine")
    private var agent: MobileAgent?
    private var discovery: Discovery?
    private var pending: [URL] = []

    func start() {
        queue.async { [self] in
            guard agent == nil else { return }
            do {
                let fm = FileManager.default
                let support = try fm.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
                    .appendingPathComponent("ephdrop", isDirectory: true)
                try fm.createDirectory(at: support, withIntermediateDirectories: true)
                // Documents shows up in the Files app under "On My iPhone > ephdrop".
                let docs = try fm.url(for: .documentDirectory, in: .userDomainMask, appropriateFor: nil, create: true)

                var failure: NSError?
                guard let a = MobileStart(support.path, UIDevice.current.name, docs.path, NativeBridge(), &failure) else {
                    throw failure ?? NSError(domain: "ephdrop", code: 1, userInfo: [NSLocalizedDescriptionKey: "ephdrop could not start"])
                }
                agent = a
                let d = Discovery(agent: a)
                discovery = d
                d.start()
                let url = URL(string: a.pageURL())
                DispatchQueue.main.async {
                    self.error = nil
                    self.pageURL = url
                }
                flushPending()
            } catch {
                DispatchQueue.main.async { self.error = error.localizedDescription }
            }
        }
    }

    func stop() {
        queue.async { [self] in
            discovery?.stop()
            discovery = nil
            agent?.stop()
            agent = nil
        }
    }

    /// Sends a file that another app shared with us into ephdrop.
    func share(fileURL: URL) {
        queue.async { [self] in
            pending.append(fileURL)
            if agent == nil {
                DispatchQueue.main.async { self.start() }
            } else {
                flushPending()
            }
        }
    }

    // Runs on `queue`.
    private func flushPending() {
        guard let a = agent else { return }
        let files = pending
        pending = []
        let port = Int(a.port())
        let token = a.token()
        for file in files {
            let scoped = file.startAccessingSecurityScopedResource()
            var name = file.lastPathComponent
            if name.isEmpty { name = "file" }
            var comps = URLComponents()
            comps.scheme = "http"
            comps.host = "127.0.0.1"
            comps.port = port
            comps.path = "/api/upload"
            comps.queryItems = [URLQueryItem(name: "name", value: name), URLQueryItem(name: "ttlHours", value: "24")]
            guard let url = comps.url else { continue }
            var req = URLRequest(url: url)
            req.httpMethod = "PUT"
            req.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
            req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
            URLSession.shared.uploadTask(with: req, fromFile: file) { _, _, _ in
                if scoped { file.stopAccessingSecurityScopedResource() }
            }.resume()
        }
    }
}
