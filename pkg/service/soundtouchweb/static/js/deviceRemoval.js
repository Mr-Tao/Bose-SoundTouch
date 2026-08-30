export async function removeDeviceAndRefresh({
    id,
    name,
    remove,
    refresh,
    showDeviceList,
    notify,
}) {
    let response;
    try {
        response = await remove(id);
    } catch (_) {
        notify('Failed to remove device');
        return false;
    }

    if (!response?.success) {
        notify(response?.error || 'Failed to remove device');
        return false;
    }

    showDeviceList();
    try {
        await refresh();
    } catch (_) {
        notify(`Removed "${name}", but failed to refresh devices`);
        return true;
    }

    notify(`Removed "${name}"`);
    return true;
}
