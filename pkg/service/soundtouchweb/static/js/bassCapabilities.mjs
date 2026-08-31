const FALLBACK = Object.freeze({ min: -9, max: 0, defaultValue: 0 });

function integer(value) {
    return Number.isInteger(value) ? value : null;
}

function control(min, max, defaultValue, current) {
    const value = integer(current) ?? defaultValue;

    return {
        available: true,
        min,
        max,
        defaultValue,
        value: Math.max(min, Math.min(max, value)),
    };
}

export function bassControlForStatus(status) {
    const capabilities = status?.bassCapabilities;
    const current = status?.bass?.ActualBass;

    if (capabilities != null) {
        const min = integer(capabilities.BassMin);
        const max = integer(capabilities.BassMax);
        const defaultValue = integer(capabilities.BassDefault);
        const availabilityKnown = capabilities.BassAvailable === true
            || capabilities.BassAvailable === false;
        if (availabilityKnown && min != null && max != null && defaultValue != null
            && min <= defaultValue && defaultValue <= max) {
            if (!capabilities.BassAvailable) {
                return { available: false };
            }

            return control(min, max, defaultValue, current);
        }
    }

    if (status?.bass == null) {
        return { available: false };
    }

    return control(FALLBACK.min, FALLBACK.max, FALLBACK.defaultValue, current);
}
