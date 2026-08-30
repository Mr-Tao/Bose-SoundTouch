function count(value, fallback = 0) {
    return Number.isInteger(value) && value >= 0 ? value : fallback;
}

function physicalMembers(zone) {
    return (zone?.members || []).flatMap(member => member?.physicalMembers || []);
}

function connectivityPresentation(member) {
    const reported = member?.connectivity;
    const connectivity = ['online', 'stale', 'offline'].includes(reported)
        ? reported
        : (member?.available ? 'online' : 'offline');

    return {
        connectivity,
        connectivityLabel: connectivity.charAt(0).toUpperCase() + connectivity.slice(1),
    };
}

function memberIPAddress(member) {
    const direct = member?.ip || member?.controlId || '';
    if (member?.ip && member.ip !== member.controlId) return member.ip;

    const physical = member?.physicalMembers || [];
    const representative = physical.find(candidate => candidate.deviceId === member?.hwId) || physical[0];

    return representative?.ip || direct;
}

export function zoneTopologyVersion(zone) {
    if (!zone) return 'standalone';

    const members = (zone.members || []).map(member => {
        const deviceIds = [...(member?.deviceIds || [])].sort();
        const physicalMembers = (member?.physicalMembers || []).map(physical => [
            physical?.deviceId || '',
            physical?.role || '',
        ]).sort((left, right) => left.join('\0').localeCompare(right.join('\0')));

        return [
            member?.controlId || '',
            member?.kind || '',
            deviceIds,
            physicalMembers,
        ];
    }).sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right)));

    return JSON.stringify([
        zone.masterControlId || '',
        zone.masterDeviceId || '',
        members,
    ]);
}

export function zoneRefreshContext(current, deviceId, topologyVersion) {
    if (current?.deviceId === deviceId && current?.topologyVersion === topologyVersion) {
        return current;
    }

    return { deviceId, topologyVersion, generation: 0 };
}

export function isCurrentZoneRefresh(context, current, generation) {
    return context === current && generation === context.generation;
}

export function zoneCardPresentation(zone) {
    const members = zone?.members || [];
    const memberCount = count(zone?.memberCount, members.length);
    const availableCount = count(
        zone?.availableMemberCount,
        members.filter(member => member?.available).length,
    );
    const degraded = Boolean(zone?.degraded || availableCount < memberCount);
    const physical = physicalMembers(zone);
    const physicalCount = count(zone?.physicalMemberCount, physical.length || memberCount);
    const availablePhysicalCount = physical.length > 0
        ? physical.filter(member => member?.available).length
        : physicalCount;

    let availabilityLabel = '';
    let availabilityTitle = `All ${memberCount} available`;
    if (degraded && availableCount < memberCount) {
        availabilityLabel = `${availableCount}/${memberCount} available`;
        availabilityTitle = `${memberCount - availableCount} unavailable`;
    } else if (degraded && availablePhysicalCount < physicalCount) {
        availabilityLabel = `${availablePhysicalCount}/${physicalCount} speakers available`;
        availabilityTitle = `${physicalCount - availablePhysicalCount} physical speaker unavailable`;
    } else if (degraded) {
        availabilityLabel = 'Degraded';
        availabilityTitle = 'The speaker topology reports a degraded state';
    }

    const health = degraded ? 'degraded' : 'healthy';

    return {
        groupLabel: `Group · ${memberCount}`,
        availabilityLabel,
        availabilityTitle,
        health,
        healthLabel: degraded ? `Degraded group: ${availabilityTitle}` : 'Healthy group',
    };
}

export function zoneMemberCountSummary(logicalCount, physicalCount) {
    const logical = count(logicalCount);
    const physical = count(physicalCount, logical);
    const logicalLabel = `${logical} ${logical === 1 ? 'member' : 'members'}`;

    if (logical === physical) return logicalLabel;

    return `${logicalLabel} · ${physical} ${physical === 1 ? 'speaker' : 'speakers'}`;
}

export function zoneMemberMetadata(member) {
    const connectivity = connectivityPresentation(member);
    const name = member?.name || member?.controlId || member?.ip || 'Unknown member';

    return {
        ...connectivity,
        name,
        modelType: member?.model || member?.type || 'Unknown model',
        ip: memberIPAddress(member),
        kind: member?.kind === 'stereoPair' ? 'Stereo pair' : 'Speaker',
        statusAriaLabel: `${name}: ${connectivity.connectivityLabel}`,
    };
}

export function physicalMemberMetadata(member) {
    const metadata = zoneMemberMetadata(member);

    return {
        ...metadata,
        role: (member?.role || 'Member').toUpperCase(),
    };
}
