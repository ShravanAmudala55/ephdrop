# Security

ephdrop moves files between devices on a home network, so security reports matter.

## Reporting a problem

Please do not open a public issue for a security problem.

Use GitHub's **Report a vulnerability** button on the Security tab of this repository. If that is not available, email shravan.amudala55@gmail.com with "ephdrop security" in the subject.

Tell us what you found, how to reproduce it, and which devices and versions. You will get a reply within a few days. Once a fix is out, you are credited if you want to be.

## What counts

Anything that lets someone who is not paired with a device:

- see, read, change or delete its files,
- pair without the person holding the code agreeing,
- make the app connect to a place outside the local network,
- run code on a device,

or that lets a web page or another app on the same device use the local API without permission.

## What the design promises

The longer description is in [docs/design.md](docs/design.md#security-notes). In short: each device has its own key, devices only talk to devices they paired with, pairing codes work once and expire after five minutes, and nothing is sent over the internet.

Not protected: someone who already has your unlocked device, and anyone you pair with can see the list of files you share.

## Versions

The project is young. Only the latest commit on `main` gets fixes.
