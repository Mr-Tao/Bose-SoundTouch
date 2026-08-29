import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { resolvedZoneMember } from '../devicePresentation.mjs';
import {
    physicalMemberMetadata,
    zoneMemberCountSummary,
    zoneMemberMetadata,
} from '../zonePresentation.mjs';
import { MemberSettings } from './MemberSettings.js';
import { ZoneMemberVolumeControl } from './ZoneMemberVolumeControl.js';

const html = htm.bind(h);

function currentSourceAllowsMultiroom(device) {
    const nowPlaying = device?.status?.nowPlaying;
    const source = nowPlaying?.Source;
    if (!source || source === 'STANDBY' || source === 'INVALID_SOURCE') return false;

    return (device?.status?.sources?.SourceItem || []).some(item =>
        item.Source === source && item.MultiroomAllowed &&
        (!nowPlaying.SourceAccount || item.SourceAccount === nowPlaying.SourceAccount));
}

function deduplicateMembers(members) {
    const seen = new Set();

    return members.filter(({ controlId, member }) => {
        const key = controlId || member?.deviceIds?.join('|');
        if (!key || seen.has(key)) return false;
        seen.add(key);
        return true;
    });
}

function inferredPhysicalCount(members) {
    return members.reduce((total, { member }) =>
        total + Math.max(1, member?.physicalMembers?.length || 0), 0);
}

export function Zone({ deviceId, devices, volumePreview = null }) {
    const [zone, setZone] = useState(null);
    const [loading, setLoading] = useState(true);
    const [showPicker, setShowPicker] = useState(false);
    const mounted = useRef(true);
    const refreshGeneration = useRef(0);
    const canGroup = currentSourceAllowsMultiroom(devices?.[deviceId]);

    function refresh() {
        if (!mounted.current) return;

        const generation = ++refreshGeneration.current;
        api.zone(deviceId).then(resp => {
            if (generation === refreshGeneration.current && resp.success) setZone(resp.data);
        }).finally(() => {
            if (generation === refreshGeneration.current) setLoading(false);
        });
    }

    useEffect(() => {
        mounted.current = true;
        refresh();
        return () => {
            mounted.current = false;
            refreshGeneration.current++;
        };
    }, [deviceId]);
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
    const resolvedMembers = (zone.members || []).map(member => resolvedZoneMember(projection, member));
    const logicalMembers = deduplicateMembers([
        ...(resolvedMaster.controlId ? [resolvedMaster] : []),
        ...resolvedMembers,
    ]);
    const physicalCount = Number.isInteger(zone.physicalMemberCount)
        ? zone.physicalMemberCount
        : (Number.isInteger(projection?.physicalMemberCount)
            ? projection.physicalMemberCount
            : inferredPhysicalCount(logicalMembers));
    const zoneMasterId = projection?.masterControlId || resolvedMaster.controlId || zone.masterIp || deviceId;

    // Devices not already in the zone are available to add.
    const zoneIds = new Set(logicalMembers.map(member => member.controlId).filter(Boolean));
    const selectedHardwareId = devices?.[deviceId]?.info?.device_id;
    const available = Object.entries(devices || {}).filter(([ip, candidate]) =>
        ip !== deviceId && candidate.info?.device_id !== selectedHardwareId &&
        !zoneIds.has(ip) && candidate.status?.isConnected);
    const deviceName = (ip) => devices[ip]?.info?.name || ip;

    function logicalMemberRow(resolved, isMaster) {
        const member = resolved.member;
        const metadata = zoneMemberMetadata(member);
        const isStereoPair = member?.kind === 'stereoPair';
        const previewVolume = volumePreview?.[resolved.controlId];

        return html`
            <div class="zone-logical-member" key=${resolved.controlId}>
                <div class="zone-logical-header">
                    <span class="device-indicator ${metadata.connectivity}" role="status"
                          title=${metadata.label} aria-label=${metadata.statusAriaLabel}></span>
                    <div class="zone-logical-identity">
                        <div class="zone-logical-name">
                            ${metadata.name}
                            ${isMaster ? html`<span class="zone-badge master">Master</span>` : null}
                        </div>
                        <div class="zone-logical-metadata">
                            <span>${metadata.type}</span>
                            ${metadata.ip ? html`<span class="zone-logical-ip">${metadata.ip}</span>` : null}
                            <span>${metadata.kind}</span>
                        </div>
                    </div>
                </div>

                <${ZoneMemberVolumeControl} zoneMasterId=${zoneMasterId}
                    memberId=${resolved.controlId} ariaLabel=${metadata.volumeAriaLabel}
                    available=${member?.available} volume=${metadata.volume}
                    previewVolume=${previewVolume} />

                ${isStereoPair ? html`
                    <div class="zone-physical-members">
                        ${(member.physicalMembers || []).map(physical => {
                            const diagnostic = physicalMemberMetadata(physical);
                            return html`
                                <div class="zone-physical-member" key=${physical.deviceId || diagnostic.role}>
                                    <span class="zone-physical-role">${diagnostic.role}</span>
                                    <span class="device-indicator ${diagnostic.connectivity}" role="status"
                                          title=${diagnostic.label}
                                          aria-label=${diagnostic.statusAriaLabel}></span>
                                    <div class="zone-physical-identity">
                                        <span class="zone-physical-name">${diagnostic.name}</span>
                                        <span class="zone-physical-metadata">
                                            <span>${diagnostic.type}</span>
                                            ${diagnostic.ip ? html`<span class="zone-logical-ip">${diagnostic.ip}</span>` : null}
                                        </span>
                                    </div>
                                </div>
                            `;
                        })}
                    </div>
                ` : null}

                <${MemberSettings}
                    controlId=${resolved.controlId}
                    member=${member}
                    fallbackName=${metadata.name}
                />
            </div>
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

            ${!zone.isStandalone && logicalMembers.length > 0 ? html`
                <details class="zone-member-details">
                    <summary>${zoneMemberCountSummary(logicalMembers.length, physicalCount)}</summary>
                    <div class="zone-logical-members">
                        ${logicalMembers.map((member, index) =>
                            logicalMemberRow(member, index === 0 && member.controlId === resolvedMaster.controlId))}
                    </div>
                </details>
            ` : null}

            ${zone.isMaster && html`
                <div class="zone-management">
                    <div class="zone-management-title">Zone management</div>
                    ${resolvedMembers.length > 0 ? html`
                        <div class="zone-management-members">
                            ${resolvedMembers.map(resolved => html`
                                <div class="zone-management-member" key=${resolved.controlId}>
                                    <span>${resolved.name || deviceName(resolved.controlId)}</span>
                                    <button class="btn-icon zone-remove" title="Remove from zone"
                                        aria-label=${`Remove ${resolved.name || deviceName(resolved.controlId)} from zone`}
                                        onClick=${() => removeDevice(resolved.controlId)}>✕</button>
                                </div>
                            `)}
                        </div>
                    ` : null}
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
                <div class="zone-management">
                    <div class="zone-management-title">Zone management</div>
                    <div class="zone-row">
                        <span class="zone-status-label">Zone: ${zone.masterName || deviceName(zone.masterIp)}</span>
                        <button class="btn-secondary zone-btn" onClick=${leave}>Leave zone</button>
                    </div>
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
