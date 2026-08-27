import { h, htm, useEffect, useState } from '../dependencies.js';
import { api } from '../api.js';
import {
    resolvedZoneMember,
    zoneMemberPresentation,
} from '../devicePresentation.mjs';

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
    const [loading, setLoading] = useState(true);
    const [showPicker, setShowPicker] = useState(false);
    const canGroup = currentSourceAllowsMultiroom(devices?.[deviceId]);

    function refresh() {
        api.zone(deviceId).then(resp => {
            if (resp.success) setZone(resp.data);
        }).finally(() => setLoading(false));
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

    const projection = devices?.[zone.masterIp || deviceId]?.zone || devices?.[deviceId]?.zone;
    const resolvedMaster = resolvedZoneMember(projection, zone.master);

    // Devices not already in the zone are available to add
    const zoneIps = new Set([
        resolvedMaster.controlId || zone.masterIp,
        ...(zone.members || []).map(member => resolvedZoneMember(projection, member).controlId),
    ].filter(Boolean));
    const selectedHardwareId = devices?.[deviceId]?.info?.device_id;
    const available = Object.entries(devices || {}).filter(([ip, candidate]) =>
        ip !== deviceId && candidate.info?.device_id !== selectedHardwareId &&
        !zoneIps.has(ip) && candidate.status?.isConnected);
    const deviceName = (ip) => devices[ip]?.info?.name || ip;
    function memberStatus(member) {
        const { connectivity, label, role, volume } = zoneMemberPresentation(
            member);

        return html`
            <span class="device-indicator ${connectivity}" role=${role} title=${label}
                  aria-label=${label}></span>
            ${volume !== null ? html`
                <span class="zone-member-volume" title=${`Volume ${volume}%`}
                      aria-label=${`Volume ${volume}%`}>${volume}%</span>
            ` : null}
        `;
    }

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
                        <span class="zone-member-name">${resolvedMaster.name || deviceName(deviceId)}</span>
                        ${memberStatus(resolvedMaster.member)}
                    </div>
                    ${(zone.members || []).map(m => {
                        const resolved = resolvedZoneMember(projection, m);
                        const controlId = resolved.controlId;
                        return html`
                            <div class="zone-member" key=${m.hwId || controlId}>
                                <span class="zone-badge slave">Member</span>
                                <span class="zone-member-name">${resolved.name || deviceName(controlId)}</span>
                                ${memberStatus(resolved.member)}
                                <button class="btn-icon zone-remove" title="Remove from zone"
                                    onClick=${() => removeDevice(controlId)}>✕</button>
                            </div>
                        `;
                    })}
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
                    ${memberStatus(zone.master)}
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
