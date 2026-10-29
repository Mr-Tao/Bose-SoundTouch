import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { api } from './api.js';
import { createPresetMutations } from './presetMutations.mjs';

export function usePresetMutations({ deviceId, device, onStatusReadback }) {
    const owner = JSON.stringify([deviceId, device?.info?.device_id, device?.status?.epoch]);
    const current = useRef(owner);
    current.current = owner; // Fence during render, before old effects clean up.
    const merge = useRef(onStatusReadback);
    merge.current = onStatusReadback;
    const [state, setState] = useState(null);
    const controller = useMemo(() => createPresetMutations({
        api, deviceId, physicalId: device?.info?.device_id, epoch: device?.status?.epoch,
        isCurrent: () => current.current === owner,
        onReadback: (...args) => merge.current?.(...args),
        onChange: next => setState(next ? { ...next, owner } : null),
    }), [owner]);
    useEffect(() => () => controller.dispose(), [controller]);
    return { ...controller, state: state?.owner === owner ? state : null };
}
