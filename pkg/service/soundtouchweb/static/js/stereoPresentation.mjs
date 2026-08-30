export function isSoundTouch10StereoPair(device) {
    if (!device?.stereoPair) return false;

    const model = String(device?.info?.type || '').toLowerCase().replace(/[^a-z0-9]/g, '');
    return model === 'st10' || model === 'soundtouch10';
}
