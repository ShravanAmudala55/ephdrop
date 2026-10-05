import Foundation
import Network
import Mobile

/// Announces this device and finds the others with the system's Bonjour. What
/// it hears goes to the Go program with `seen`. The Go program forgets a device
/// it has not heard from for two minutes, so everything known is reported again
/// every 30 seconds.
final class Discovery {
    private struct Known {
        let id: String
        let ips: String
        let port: Int
        let version: String
    }

    private let agent: MobileAgent
    private let queue = DispatchQueue(label: "ephdrop.discovery")
    private var browser: NWBrowser?
    private var service: NetService?
    private var known: [NWEndpoint: Known] = [:]
    private var resolving: [NWEndpoint: NWConnection] = [:]
    private var timer: DispatchSourceTimer?

    init(agent: MobileAgent) {
        self.agent = agent
    }

    func start() {
        let id = agent.deviceID()
        let port = Int32(agent.transferPort())

        let b = NWBrowser(for: .bonjourWithTXTRecord(type: "_ephdrop._tcp", domain: nil), using: .tcp)
        b.browseResultsChangedHandler = { [weak self] _, changes in
            self?.handle(changes)
        }
        b.start(queue: queue)
        browser = b

        // NetService only announces a port that the Go program is already listening on.
        DispatchQueue.main.async { [weak self] in
            let s = NetService(domain: "", type: "_ephdrop._tcp.", name: "ephdrop-" + String(id.prefix(12)), port: port)
            s.setTXTRecord(NetService.data(fromTXTRecord: ["v": Data("1".utf8), "id": Data(id.utf8)]))
            s.publish()
            self?.service = s
        }

        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 30, repeating: 30)
        t.setEventHandler { [weak self] in
            guard let self else { return }
            for k in self.known.values { self.report(k) }
        }
        t.resume()
        timer = t
    }

    func stop() {
        timer?.cancel()
        timer = nil
        browser?.cancel()
        browser = nil
        let s = service
        DispatchQueue.main.async { s?.stop() }
        service = nil
        queue.async { [self] in
            for c in resolving.values { c.cancel() }
            resolving.removeAll()
            known.removeAll()
        }
    }

    // MARK: finding

    private func handle(_ changes: Set<NWBrowser.Result.Change>) {
        for change in changes {
            switch change {
            case .added(let r), .changed(old: _, new: let r, flags: _):
                resolve(r)
            case .removed(let r):
                if let k = known.removeValue(forKey: r.endpoint) {
                    agent.gone(k.id)
                }
            default:
                break
            }
        }
    }

    /// Works out the address behind a Bonjour name by connecting to it once.
    private func resolve(_ r: NWBrowser.Result) {
        guard case .bonjour(let txt) = r.metadata,
              let id = txt["id"], let version = txt["v"],
              resolving[r.endpoint] == nil else { return }
        let endpoint = r.endpoint
        let c = NWConnection(to: endpoint, using: .tcp)
        resolving[endpoint] = c
        c.stateUpdateHandler = { [weak self, weak c] state in
            guard let self, let c else { return }
            switch state {
            case .ready:
                if case .hostPort(let host, let port)? = c.currentPath?.remoteEndpoint, case .ipv4(let ip) = host {
                    let address = ip.debugDescription.split(separator: "%").first.map(String.init) ?? ""
                    if !address.isEmpty {
                        let k = Known(id: id, ips: address, port: Int(port.rawValue), version: version)
                        self.known[endpoint] = k
                        self.report(k)
                    }
                }
                c.cancel()
            case .failed, .cancelled:
                self.resolving[endpoint] = nil
            default:
                break
            }
        }
        c.start(queue: queue)
        // do not wait forever on a device that has gone
        queue.asyncAfter(deadline: .now() + 5) { [weak c] in c?.cancel() }
    }

    private func report(_ k: Known) {
        // A refused announcement is not ours to use. Ignore it.
        try? agent.seen(k.id, ips: k.ips, port: k.port, version: k.version)
    }
}
