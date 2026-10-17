// Navigation remembers presentation only. Speaker snapshots and pending commands
// always come from the live inventory/components when a detail mounts again.
export function detailViewIdentity(device) {
    const id = device?.info?.device_id;
    if (!id) return null;
    const pair = device.stereoPair;
    return JSON.stringify([id, pair?.id || '', pair?.masterDeviceId || '',
        (pair?.members || []).map(member => [member.deviceId, member.role]).sort()]);
}

function disclosures(root) {
    return [...root.querySelectorAll('details[data-detail-key], button[data-detail-key][aria-expanded]')];
}

function expanded(node) {
    return node.tagName === 'DETAILS' ? node.open : node.getAttribute('aria-expanded') === 'true';
}

export function captureDetailView(root, device, anchor = null) {
    if (!root) return null;
    const win = root.ownerDocument.defaultView;
    const nodes = disclosures(root);
    const states = nodes.filter(node => nodes.filter(other =>
        other.dataset.detailKey === node.dataset.detailKey).length === 1)
        .map(node => [node.dataset.detailKey, expanded(node)]);
    const candidate = anchor || root.ownerDocument.activeElement;
    const focus = root.contains(candidate) ? candidate : null;
    return {
        identity: detailViewIdentity(device), states, scrollY: win.scrollY,
        focusKey: focus?.dataset.detailFocus || null,
        anchorTop: focus?.dataset.detailFocus ? focus.getBoundingClientRect().top : null,
    };
}

export function restoreDetailView(root, view, device) {
    if (!root || !view?.identity || view.identity !== detailViewIdentity(device)) return () => {};
    const win = root.ownerDocument.defaultView;
    let stopped = false;
    let frame = 0;
    let revision = 0;
    const pending = new Set();
    const wanted = new Map(view.states || []);
    const inputEvents = ['wheel', 'touchstart', 'pointerdown', 'keydown'];

    function stop() {
        if (stopped) return;
        stopped = true;
        observer.disconnect();
        win.cancelAnimationFrame(frame);
        inputEvents.forEach(type => win.removeEventListener(type, stop, true));
    }

    function visible(node) {
        return !node.closest('[hidden], details:not([open])');
    }

    function position() {
        if (stopped || !root.isConnected) { stop(); return; }
        const anchor = [...root.querySelectorAll('[data-detail-focus]')]
            .find(node => node.dataset.detailFocus === view.focusKey && visible(node));
        if (anchor && Number.isFinite(view.anchorTop)) {
            anchor.focus({ preventScroll: true });
            win.scrollTo(0, win.scrollY + anchor.getBoundingClientRect().top - view.anchorTop);
        } else {
            win.scrollTo(0, view.scrollY || 0);
        }
    }

    function apply() {
        if (stopped || !root.isConnected) return stop();
        const nodes = disclosures(root);
        let settled = true;
        for (const node of nodes) {
            const key = node.dataset.detailKey;
            if (!wanted.has(key) || nodes.filter(other => other.dataset.detailKey === key).length !== 1) continue;
            if (expanded(node) === wanted.get(key)) {
                pending.delete(node);
            } else {
                settled = false;
                if (node.tagName === 'DETAILS') node.open = wanted.get(key);
                else if (!pending.has(node)) {
                    pending.add(node);
                    node.click();
                }
            }
        }
        // Toggle events start fresh reads; hidden/collapsed settings do not
        // delay restoring the part of the page the user actually opened.
        const loading = [...root.querySelectorAll('.loading-bar, .settings-loading, [data-detail-loading="true"]')]
            .some(visible);
        position();
        if (!settled || loading) return;
        const expectedRevision = revision;
        frame = win.requestAnimationFrame(() => {
            frame = win.requestAnimationFrame(() => {
                if (stopped || revision !== expectedRevision) return;
                position();
                stop();
            });
        });
    }

    const observer = new win.MutationObserver(() => {
        revision += 1;
        win.cancelAnimationFrame(frame);
        frame = win.requestAnimationFrame(apply);
    });
    observer.observe(root, { childList: true, subtree: true, attributes: true,
        attributeFilter: ['open', 'aria-expanded', 'hidden', 'data-detail-loading'] });
    inputEvents.forEach(type => win.addEventListener(type, stop, { capture: true, passive: true }));
    frame = win.requestAnimationFrame(apply);
    return stop;
}
