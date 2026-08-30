---
title: "Spotify Priming Strategy"
---
> **New here?** Start with [spotify-overview.md](spotify-overview.md) for the
> mental model. This document goes deep on the priming protocol, ZeroConf DH
> exchange, and deployment topologies.

This document outlines the strategy for ensuring Bose SoundTouch devices are correctly "primed" for Spotify Connect integration within the AfterTouch ecosystem.

## Overview

To enable Spotify Connect for SoundTouch devices, especially for remote availability outside the local network, the speaker must be associated with a Spotify account via a process called "priming." This involves a two-step exchange with the speaker's ZeroConf API (port 8200):

1. **`getInfo`** — retrieve the speaker's Diffie-Hellman public key and device metadata.
2. **`addUser`** — push encrypted Spotify credentials using the shared DH secret.

This is the standard Spotify Connect ZeroConf protocol. The encrypted blob
establishes the Spotify identity on the speaker. Subsequent provider-token
refresh still passes through AfterTouch's Bose-compatible OAuth endpoint, which
resolves the exact configured account and refreshes its token when necessary.

### ZeroConf Protocol

The current implementation follows the full Spotify Connect ZeroConf protocol (`pkg/service/spotify/zeroconf.go`):

1. `GET http://{ip}:8200/zc?action=getInfo` → parse `publicKey` (base64 DH key, 768-bit Oakley Group 1 prime) from the response.
2. Generate a client DH key pair using the same group parameters.
3. Compute `sharedSecret = DH(clientPrivate, speakerPublicKey)`.
4. Derive keys: `baseKey = SHA1(sharedSecret)[:16]`, then HMAC-SHA1 with labels `"encryption"` and `"checksum"`.
5. Encrypt a protobuf-encoded `LoginCredentials` blob (username, `AUTHENTICATION_SPOTIFY_TOKEN=4`, access token) using AES-128-CTR + HMAC-SHA1 checksum.
6. `POST http://{ip}:8200/zc?action=addUser` with `blob={encryptedBlob}`, `clientKey={clientPublicKeyBase64}`.

The speaker decrypts the blob and stores the Spotify identity. For subsequent
access-token renewal it calls AfterTouch's Bose-compatible OAuth broker with a
non-Spotify surrogate secret. AfterTouch keeps the provider refresh token and
resolves every request through the speaker's exact configured source binding.

The algorithm is based on [librespot](https://github.com/librespot-org/librespot) (Rust reference implementation).

### Fail-closed behavior

Priming requires a typed `getInfo` response. AfterTouch does not claim success
from an `addUser` HTTP status or an empty 404: it writes at most once and then
confirms the requested `activeUser` with bounded readback. A foreign active
user is a conflict; an empty or unreadable result is unverified or failed.

AfterTouch adopts a **Server-Centric Hybrid Model** that prioritizes device cleanliness and user intent while providing automated self-healing.

## Core Principles

### 1. User Intent (Opt-in)
AfterTouch replicates the native Bose "Add Source" experience. No Spotify priming occurs until a user explicitly links their Spotify account through the AfterTouch Management Dashboard. This ensures privacy and respects users who do not wish to use Spotify.

### 2. Device Cleanliness (Minimalist Footprint)
We avoid invasive modifications to the speaker's filesystem.
- **No On-Device Scripts:** We deprecate the use of internal boot-primer scripts.
- **Native Communication:** We rely on the speaker's native ability to talk to Bose services, which are intercepted via DNS to point to the AfterTouch server.

### 3. Triggers for Priming
Priming is triggered when the speaker signals it is active and ready, specifically:

- **Power On:** When a previously known speaker with a canonical, non-default
  Marge account calls `/marge/streaming/support/power_on` from its bound peer
  address, AfterTouch ensures the device's ZeroConf state is correctly primed.
  First-seen and default-account reports update discovery metadata only.
- **Manual Override:** Users can manually trigger a "Prime Spotify" from the device list in the UI if needed.

During any of these events, the server:
1. Resolves the device's live or persisted Marge account and configured Spotify source.
2. Resolves exactly one linked Spotify identity and validates its surrogate secret.
3. Reads the current ZeroConf `activeUser`.
4. Revalidates the exact source ownership and Spotify generation immediately
   before writing credentials, then writes at most once only when `activeUser`
   is empty.
5. Reports a typed result after bounded authoritative readback.

### 4. Automated Recovery
AfterTouch ensures that if a speaker loses its session (due to a crash or power loss), it is re-primed when it next powers on and reaches out to the service.

### 5. Decoupling
The logic for account management and device interaction remains decoupled:
- **Spotify Service:** Manages OAuth tokens and account state.
- **Discovery Service:** Finds devices and tracks their network presence.
- **Orchestrator:** Connects the two, deciding when to push tokens to discovered devices based on the current link status.

## Workflow

### Initial Setup (The "Add Source" UX)
1. User opens the AfterTouch Dashboard.
2. User selects "Link Spotify Account."
3. OAuth flow completes; AfterTouch stores the token.
4. AfterTouch immediately triggers a discovery run to find and prime all compatible speakers.

### Maintenance (The "Watchdog" UX)
1. A speaker reboots or loses its token.
2. A discovery event occurs (periodic or triggered by UI).
3. AfterTouch detects the "Empty" user state on the speaker.
4. AfterTouch pushes a fresh token from the Spotify Service.
5. UI reflects that the device is "Managed by AfterTouch" and healthy.

> **Note:** Priming restores speaker identity after a reboot or lost local state.
> Normal provider token refresh is handled by AfterTouch's speaker broker and
> does not require blindly repeating `addUser`.

### Manual Override
Users can manually trigger a "Re-prime" or "Refresh Link" from the device list in the UI if they suspect the automated self-healing is delayed or if they want to force a specific account onto a device.

## Network Topology & Deployment Scenarios

The strategy adapts based on where the AfterTouch server is deployed:

### Local Deployment (Home Server / Docker)
- **Mechanism:** Both "Pull" (Marge) and "Push" (ZeroConf side-channel) are used.
- **Advantage:** The server can proactively fix the speaker's state via port 8200 as soon as it sees a "Liveness Signal."

### External Deployment (Cloud VPS)
- **Mechanism:** Primarily relies on "Pull" (Marge).
- **Constraint:** The server cannot reach port 8200 on the speaker due to NAT/Firewall.
- **Strategy:** In this scenario, AfterTouch acts as a passive token provider. The speaker must initiate the connection to our intercepted Bose endpoints to receive its Spotify configuration. If the speaker completely loses its user state and stops "pulling," a manual re-prime from a local machine or a temporary local discovery run might be required.

## Transition & Cleanup

As AfterTouch moves to the Server-Centric model, we will:
1.  **Revert On-Device Migration:** Update the Setup Manager to remove legacy `spotify-boot-primer` scripts and `rc.local` hooks from the speakers.
2.  **Consolidated Directory:** We maintain the `/mnt/nv/soundtouch-service/` base directory for other configuration needs (e.g., `aftertouch.resolv.conf`), but it will no longer contain Spotify-specific credentials or scripts.
3.  **No On-Device Credentials:** The `/mnt/nv/soundtouch-service/spotify-primer.conf` will be removed, ensuring that no sensitive AfterTouch login details are stored on the speaker in plain text.

## Implementation Roadmap

1. ✅ **Server-Side Priming Logic:** `PrimeDeviceWithSpotify(ip)` and `pushSpotifyTokenToDevice` in `pkg/service/handlers/server.go`. Triggered on device registration (marge handlers) and via the manual `HandleMgmtPrimeDevice` endpoint.
2. ✅ **Discovery Hook:** `handleDiscoveredDevice` calls `PrimeDeviceWithSpotify` when a speaker is found.
3. ✅ **Verified ZeroConf Blob:** Full DH key exchange + AES-128-CTR encrypted `LoginCredentials` blob, followed by typed `activeUser` readback.
4. ⬜ **Recovery Reconciliation:** Event-driven or bounded periodic checks for
   speakers that rebooted or lost their local Spotify identity. Normal provider
   token expiry is handled by the broker and must not trigger blind `addUser`
   writes.
5. ⬜ **Revert On-Device Migration:** Update the Setup Manager to remove legacy `spotify-boot-primer` scripts and `rc.local` hooks from the speakers.
6. ⬜ **UI Enhancements:** Update the Speaker List to show "Spotify Linked" status and provide manual refresh buttons.
