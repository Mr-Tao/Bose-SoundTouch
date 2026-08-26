import { h } from 'preact';
import { useState, useEffect } from 'preact/hooks';
import htm from 'htm';
import { api } from '../api.js';

const html = htm.bind(h);

function currentSourceAllowsMultiroom(device) {
    const nowPlaying = device?.status?.nowPlaying;
    const source = nowPlaying?.Source;
    if (!source || source === 'STANDBY' || source === 'INVALID_SOURCE') return false;

    return (device?.status?.sources?.SourceItem || []).some(item =>
        item.Source === source && item.MultiroomAllowed &&
        (!nowPlaying.SourceAccount || item.SourceAccount === nowPlaying.SourceAccount));
}

export function Zone({ deviceId, devices }) {
    const [zone, setZone] = useState(null);
    const [candidates, setCandidates] = useState({});
    const [loading, setLoading] = useState(true);
    const [showPicker, setShowPicker] = useState(false);
    const canGroup = currentSourceAllowsMultiroom(devices?.[deviceId]);

    function refresh() {
        Promise.all([api.zone(deviceId), api.zoneCandidates(deviceId)])
            .then(([zoneResp, candidatesResp]) => {
                if (zoneResp.success) setZone(zoneResp.data);
                if (candidatesResp.success) setCandidates(candidatesResp.data || {});
            })
            .finally(() => setLoading(false));
    }

    useEffect(() => { refresh(); }, [deviceId]);
    useEffect(() => {
        if (!canGroup) setShowPicker(false);
    }, [canGroup]);

    async function addDevice(slaveId) {
        if (!canGroup) return;
        setShowPicker(false);
        await api.zoneAdd(deviceId, slaveId);
        refresh();
    }

    async function removeDevice(slaveId) {
        await api.zoneRemove(deviceId, slaveId);
        refresh();
    }

    async function dissolve() {
        await api.zoneDissolve(deviceId);
        refresh();
    }

    async function leave() {
        await api.zoneLeave(deviceId);
        refresh();
    }

    if (loading) return html`
        <div class="zone-section">
            <div class="section-title">Zone</div>
            <div class="loading-bar"></div>
        </div>
    `;

    if (!zone) return null;

    // Devices not already in the zone are available to add. Sourced from a
    // dedicated candidates endpoint rather than the `devices` prop: Zone and
    // Group are separate, unrelated groupings, so a stereo pair's hidden
    // member -- absent from the collapsed device list -- is still a valid,
    // independent zone-add target.
    const zoneIps = new Set([zone.masterIp, ...(zone.members || []).map(m => m.ip)].filter(Boolean));
    const available = Object.entries(candidates).filter(([ip]) => !zoneIps.has(ip));

    const deviceName = (ip) => devices[ip]?.info?.name || candidates[ip]?.info?.name || ip;

    return html`
        <div class="zone-section">
            <div class="section-title">Zone</div>

            ${zone.isStandalone && html`
                <div class="zone-row">
                    <span class="zone-status-label">Standalone</span>
                    ${available.length > 0 && html`
                        <button class="btn-secondary zone-btn" onClick=${() => setShowPicker(true)}
                            disabled=${!canGroup}>+ Group with…</button>
                    `}
                </div>
                ${available.length > 0 && !canGroup && html`
                    <div class="zone-status-label">Start a multiroom-capable source before grouping speakers.</div>
                `}
            `}

            ${zone.isMaster && html`
                <div class="zone-members">
                    <div class="zone-member zone-master-row">
                        <span class="zone-badge master">Master</span>
                        <span class="zone-member-name">${deviceName(deviceId)}</span>
                    </div>
                    ${(zone.members || []).map(m => html`
                        <div class="zone-member" key=${m.ip}>
                            <span class="zone-badge slave">Member</span>
                            <span class="zone-member-name">${m.name || deviceName(m.ip)}</span>
                            <button class="btn-icon zone-remove" title="Remove from zone"
                                onClick=${() => removeDevice(m.ip)}>✕</button>
                        </div>
                    `)}
                    <div class="zone-actions">
                        ${available.length > 0 && html`
                            <button class="btn-secondary zone-btn" onClick=${() => setShowPicker(true)}
                                disabled=${!canGroup}>+ Add speaker</button>
                        `}
                        <button class="btn-secondary zone-btn" onClick=${dissolve}>Dissolve zone</button>
                    </div>
                </div>
            `}

            ${zone.isSlave && html`
                <div class="zone-row">
                    <span class="zone-badge slave">Member</span>
                    <span class="zone-member-name">Zone: ${zone.masterName || deviceName(zone.masterIp)}</span>
                    <button class="btn-secondary zone-btn" onClick=${leave}>Leave zone</button>
                </div>
            `}

            ${showPicker && canGroup && html`
                <div class="overlay" onClick=${() => setShowPicker(false)}>
                    <div class="device-picker" onClick=${e => e.stopPropagation()}>
                        <div class="picker-title">Add to zone</div>
                        <div class="picker-devices">
                            ${available.map(([ip, d]) => html`
                                <button class="picker-device-btn" key=${ip} onClick=${() => addDevice(ip)}>
                                    <div class="picker-device-info">
                                        <span class="picker-device-name">${d.info?.name || ip}</span>
                                        <span class="picker-device-ip">${d.info?.ip_address || ip}</span>
                                    </div>
                                </button>
                            `)}
                        </div>
                        <button class="btn-secondary picker-cancel" onClick=${() => setShowPicker(false)}>Cancel</button>
                    </div>
                </div>
            `}
        </div>
    `;
}
