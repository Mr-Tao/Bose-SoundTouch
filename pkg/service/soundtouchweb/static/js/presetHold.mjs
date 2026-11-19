export const PRESET_HOLD_INTENT_MS = 160;
export const PRESET_HOLD_FILL_MS = 1100;
export const PRESET_HOLD_MOVE_PX = 12;

// Pointer activation is handled here; native keyboard/assistive clicks stay
// with the button. No pointerdown preventDefault: vertical scrolling is native.
export function createPresetHoldGesture({ getSnapshot, onChange, onTap, onSave,
    window: win = globalThis.window, document: doc = globalThis.document }) {
    let gesture = null;
    let timer = null;
    let suppressClick = false;
    const pointers = new Set();
    const visible = button => button.isConnected && !button.closest('[hidden], [inert]') && button.getClientRects().length > 0;
    const moved = (event, g) => Math.hypot(event.clientX - g.x, event.clientY - g.y) > PRESET_HOLD_MOVE_PX;
    function cancel(message = '') {
        if (!gesture) return;
        const previous = gesture;
        gesture = null;
        clearTimeout(timer);
        timer = null;
        onChange(null, message);
        try { if (previous.button.hasPointerCapture(previous.id)) previous.button.releasePointerCapture(previous.id); } catch (_) { /* Already released. */ }
    }
    function valid(g) {
        const latest = getSnapshot();
        return gesture === g && latest.canTap && visible(g.button) && latest.owner === g.snapshot.owner &&
            latest.identity === g.snapshot.identity && (!g.snapshot.canHold || latest.canHold);
    }
    const cancelEvent = () => cancel('Saving cancelled.');
    const pointerDown = event => {
        pointers.add(event.pointerId);
        if (pointers.size > 1 || event.isPrimary === false) cancelEvent();
    };
    const pointerEnd = event => pointers.delete(event.pointerId);
    const leave = () => { pointers.clear(); cancelEvent(); };
    const visibility = () => { if (doc.hidden) leave(); };
    const methods = {
        attach() {
            const observer = new win.MutationObserver(() => methods.check());
            observer.observe(doc.documentElement, { subtree: true, attributes: true, attributeFilter: ['hidden', 'inert'] });
            doc.addEventListener('pointerdown', pointerDown, true);
            doc.addEventListener('pointerup', pointerEnd, true);
            doc.addEventListener('pointercancel', pointerEnd, true);
            doc.addEventListener('scroll', cancelEvent, true);
            doc.addEventListener('visibilitychange', visibility);
            win.addEventListener('blur', leave);
            return () => {
                observer.disconnect();
                cancel();
                pointers.clear();
                doc.removeEventListener('pointerdown', pointerDown, true);
                doc.removeEventListener('pointerup', pointerEnd, true);
                doc.removeEventListener('pointercancel', pointerEnd, true);
                doc.removeEventListener('scroll', cancelEvent, true);
                doc.removeEventListener('visibilitychange', visibility);
                win.removeEventListener('blur', leave);
            };
        },
        check() { if (gesture && !valid(gesture)) cancelEvent(); },
        cancel,
        down(event, slot) {
            suppressClick = true;
            cancel();
            const snapshot = getSnapshot();
            if (event.button !== 0 || event.isPrimary === false || pointers.size > 1 || !snapshot.canTap || !visible(event.currentTarget)) return;
            const g = gesture = { id: event.pointerId, button: event.currentTarget, slot, x: event.clientX, y: event.clientY,
                started: win.performance.now(), snapshot, item: snapshot.current ? { ...snapshot.current } : null };
            try { g.button.setPointerCapture(g.id); } catch (_) { cancel(); return; }
            if (!snapshot.canHold) return;
            timer = setTimeout(() => {
                if (!valid(g)) { cancelEvent(); return; }
                onChange({ slot, phase: 'holding', item: g.item }, `Hold to save “${g.item.ItemName || 'current playback'}” to preset ${slot}.`);
                timer = setTimeout(() => {
                    if (!valid(g)) { cancelEvent(); return; }
                    g.ready = true;
                    onChange({ slot, phase: 'ready', item: g.item }, `Release to save “${g.item.ItemName || 'current playback'}” to preset ${slot}.`);
                }, PRESET_HOLD_FILL_MS);
            }, PRESET_HOLD_INTENT_MS);
        },
        move(event) {
            if (gesture?.id === event.pointerId && moved(event, gesture)) cancelEvent();
        },
        up(event) {
            const g = gesture;
            if (!g || g.id !== event.pointerId) return;
            const elapsed = win.performance.now() - g.started;
            const rect = g.button.getBoundingClientRect();
            const inside = event.clientX >= rect.left && event.clientX <= rect.right && event.clientY >= rect.top && event.clientY <= rect.bottom;
            const allowed = valid(g) && inside && !moved(event, g);
            cancel();
            if (!allowed) return;
            if (g.ready) onSave(g.slot, g.item);
            else if (elapsed < PRESET_HOLD_INTENT_MS || !g.snapshot.canHold) onTap(g.slot);
            else onChange(null, 'Saving cancelled.');
        },
        click(event, slot) {
            // Browser pointer clicks follow our pointerup action. detail=0
            // without a pointerType is native keyboard/assistive activation.
            if (event.detail === 0 && !event.pointerType) {
                cancel();
                if (getSnapshot().canTap) onTap(slot);
                return;
            }
            if (!suppressClick && getSnapshot().canTap) onTap(slot);
            suppressClick = false;
        },
        interrupted: cancelEvent,
    };
    return methods;
}
