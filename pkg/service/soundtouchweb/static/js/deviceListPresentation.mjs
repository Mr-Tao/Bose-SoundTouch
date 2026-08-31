const compareOptions = { numeric: true, sensitivity: 'base' };

export function deviceAddress(controlID, device) {
    const address = device?.info?.ip_address?.trim();
    return address || controlID;
}

export function sortDeviceEntries(entries, mode) {
    const copy = [...entries];

    copy.sort(([controlIDA, deviceA], [controlIDB, deviceB]) => {
        const addressA = deviceAddress(controlIDA, deviceA);
        const addressB = deviceAddress(controlIDB, deviceB);

        if (mode === 'name') {
            const nameA = deviceA?.info?.name || addressA;
            const nameB = deviceB?.info?.name || addressB;
            const byName = nameA.localeCompare(nameB, undefined, { sensitivity: 'base' });
            if (byName !== 0) return byName;
        }

        const byAddress = addressA.localeCompare(addressB, undefined, compareOptions);
        if (byAddress !== 0) return byAddress;

        return controlIDA.localeCompare(controlIDB, undefined, compareOptions);
    });

    return copy;
}
