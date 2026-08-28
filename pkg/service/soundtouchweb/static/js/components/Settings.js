import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import {
    clockControls,
    clockDisplayPatch,
    deviceSettingsTitle,
    settingsSections,
} from '../settingsPresentation.mjs';

const html = htm.bind(h);

const SECTION_ERROR_KEYS = {
    clock: ['clock', 'clockDisplay', 'clockTime'],
    standby: ['standby', 'systemTimeout'],
    language: ['language'],
    sync: ['sync'],
    bluetooth: ['bluetooth', 'bluetoothPair', 'bluetoothClear'],
    sources: ['sources', 'sourceNaming'],
    network: ['network', 'wifiOnboarding'],
};

function errorText(error, fallback) {
    if (typeof error === 'string' && error) return error;
    if (typeof error?.message === 'string' && error.message) return error.message;
    return fallback;
}

function snapshotErrors(errors, section) {
    if (!errors) return [];
    if (typeof errors === 'string') return [errors];
    if (Array.isArray(errors)) {
        return errors.map(error => errorText(error, '')).filter(Boolean);
    }

    return (SECTION_ERROR_KEYS[section] || [section]).flatMap(key => {
        const value = errors[key];
        if (!value) return [];
        return (Array.isArray(value) ? value : [value])
            .map(error => errorText(error, ''))
            .filter(Boolean);
    });
}

function SectionErrors({ snapshot, section, actionError }) {
    const messages = [actionError, ...snapshotErrors(snapshot?.errors, section)].filter(Boolean);
    if (messages.length === 0) return null;

    return html`
        <div class="settings-error" role="alert">
            ${messages.map((message, index) => html`<div key=${`${section}-${index}`}>${message}</div>`)}
        </div>
    `;
}

function Toggle({ checked, disabled, label, onChange }) {
    return html`
        <label class="settings-toggle">
            <input
                type="checkbox"
                checked=${Boolean(checked)}
                disabled=${disabled}
                onChange=${event => onChange(event.target.checked)}
            />
            <span class="settings-toggle-track" aria-hidden="true"></span>
            <span>${label}</span>
        </label>
    `;
}

function SourceRenameRow({ source, busy, onRename }) {
    const [name, setName] = useState(source.displayName || '');

    useEffect(() => {
        setName(source.displayName || '');
    }, [source.displayName, source.source, source.sourceAccount]);

    const trimmedName = name.trim();
    const unchanged = trimmedName === (source.displayName || '');

    function submit(event) {
        event.preventDefault();
        if (!trimmedName || unchanged || busy) return;
        onRename(source.source, source.sourceAccount || '', trimmedName);
    }

    return html`
        <form class="settings-source-row" onSubmit=${submit}>
            <label>
                <span>${source.source}${source.sourceAccount ? ` · ${source.sourceAccount}` : ''}</span>
                <input
                    type="text"
                    value=${name}
                    disabled=${busy}
                    onInput=${event => setName(event.target.value)}
                    aria-label=${`${source.source} display name`}
                />
            </label>
            <button
                type="submit"
                class="btn-secondary settings-action"
                disabled=${busy || !trimmedName || unchanged}
            >Rename</button>
        </form>
    `;
}

function networkInterfaces(network) {
    if (Array.isArray(network?.interfaces)) return network.interfaces;
    if (Array.isArray(network?.interfaces?.interfaces)) return network.interfaces.interfaces;
    return [];
}

function interfaceName(item) {
    if (item.type === 'WIFI_INTERFACE') return item.ssid ? `Wi-Fi · ${item.ssid}` : 'Wi-Fi';
    if (item.type === 'ETHERNET_INTERFACE') return 'Ethernet';
    return item.name || item.type || 'Network interface';
}

function statusLabel(value) {
    return value ? String(value).replace(/^NETWORK_/, '').replace(/_/g, ' ').toLowerCase() : '';
}

export function Settings({ deviceId, targetName = '' }) {
    const [snapshot, setSnapshot] = useState(null);
    const [loading, setLoading] = useState(false);
    const [loadError, setLoadError] = useState('');
    const [busy, setBusy] = useState('');
    const [actionErrors, setActionErrors] = useState({});
    const requested = useRef(false);

    useEffect(() => {
        setSnapshot(null);
        setLoadError('');
        setBusy('');
        setActionErrors({});
        requested.current = false;
    }, [deviceId]);

    async function load() {
        if (requested.current) return;
        requested.current = true;
        setLoading(true);
        setLoadError('');
        try {
            const response = await api.settings(deviceId);
            if (!response?.success || !response.data) {
                throw new Error(response?.error || 'The speaker did not return its settings.');
            }
            setSnapshot(response.data);
        } catch (error) {
            setLoadError(errorText(error, 'Could not load settings. Check that the speaker is reachable.'));
            requested.current = false;
        } finally {
            setLoading(false);
        }
    }

    async function mutate(section, action, fallback) {
        if (busy) return;
        setBusy(section);
        setActionErrors(previous => ({ ...previous, [section]: '' }));
        try {
            const response = await action();
            if (response?.outcome === 'unverified') {
                if (response.data) setSnapshot(response.data);
                throw new Error(response.error || 'The speaker accepted the command, but its result could not be verified.');
            }
            if (!response?.success || !response.data || response.error) {
                throw new Error(response?.error || fallback);
            }
            setSnapshot(response.data);
        } catch (error) {
            setActionErrors(previous => ({
                ...previous,
                [section]: errorText(error, fallback),
            }));
        } finally {
            setBusy('');
        }
    }

    function onToggle(event) {
        if (event.currentTarget.open && !snapshot && !loading) load();
    }

    const sections = settingsSections(snapshot);
    const support = snapshot?.support || {};
    const clock = snapshot?.clockDisplay || {};
    const clockControl = clockControls(snapshot);
    const interfaces = networkInterfaces(snapshot?.network);
    const browserTimeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

    function updateClockDisplay(field, value, fallback) {
        const patch = clockDisplayPatch(snapshot, field, value);
        if (!patch) return;
        mutate('clock', () => api.setClockDisplay(deviceId, patch), fallback);
    }

    return html`
        <details class="settings-section" onToggle=${onToggle}>
            <summary class="settings-summary">
                <span class="section-title">${deviceSettingsTitle(targetName)}</span>
                <span class="settings-chevron" aria-hidden="true"></span>
            </summary>

            <div class="settings-content">
                ${loading ? html`<div class="settings-loading" role="status">Loading settings…</div>` : null}
                ${loadError ? html`
                    <div class="settings-load-error" role="alert">
                        <span>${loadError}</span>
                        <button class="btn-secondary settings-action" onClick=${load}>Retry</button>
                    </div>
                ` : null}

                ${snapshot && sections.length === 0 ? html`
                    <p class="settings-empty">This speaker does not report supported settings.</p>
                ` : null}

                ${sections.includes('clock') ? html`
                    <section class="settings-group">
                        <h4>Clock</h4>
                        <${SectionErrors} snapshot=${snapshot} section="clock" actionError=${actionErrors.clock} />
                        ${clockControl.display ? html`
                            <${Toggle}
                                checked=${clock.enabled}
                                disabled=${busy === 'clock'}
                                label="Show clock display"
                                onChange=${enabled => updateClockDisplay(
                                    'enabled',
                                    enabled,
                                    'Could not update the clock display.',
                                )}
                            />
                        ` : null}
                        ${clockControl.format ? html`
                            <label class="settings-field">
                                <span>Time format</span>
                                <select
                                    value=${clock.format}
                                    disabled=${busy === 'clock'}
                                    onChange=${event => updateClockDisplay(
                                        'format',
                                        event.target.value,
                                        'Could not update the time format.',
                                    )}
                                >
                                    <option value="12">12-hour</option>
                                    <option value="24">24-hour</option>
                                    <option value="auto">Automatic</option>
                                </select>
                            </label>
                        ` : null}
                        ${clockControl.timeZone ? html`
                            <div class="settings-command-row">
                                <div>
                                    <span class="settings-command-label">Timezone</span>
                                    <span class="settings-value">${clock.timeZone}</span>
                                </div>
                                <button
                                    class="btn-secondary settings-action"
                                    disabled=${busy === 'clock' || !browserTimeZone}
                                    onClick=${() => updateClockDisplay(
                                        'timeZone',
                                        browserTimeZone,
                                        'Could not update the timezone.',
                                    )}
                                >Use browser timezone</button>
                            </div>
                        ` : null}
                        ${clockControl.currentTime ? html`
                            <button
                                class="btn-secondary settings-action"
                                disabled=${busy === 'clock'}
                                onClick=${() => mutate(
                                    'clock',
                                    () => api.setClockTime(deviceId),
                                    'Could not set the speaker clock.',
                                )}
                            >Set clock now</button>
                        ` : null}
                    </section>
                ` : null}

                ${sections.includes('standby') ? html`
                    <section class="settings-group">
                        <h4>Automatic standby</h4>
                        <${SectionErrors} snapshot=${snapshot} section="standby" actionError=${actionErrors.standby} />
                        ${snapshot.systemTimeout ? html`
                            <${Toggle}
                                checked=${snapshot.systemTimeout.enabled}
                                disabled=${busy === 'standby'}
                                label="Enable automatic standby"
                                onChange=${enabled => mutate(
                                    'standby',
                                    () => api.setSystemTimeout(deviceId, enabled),
                                    'Could not update automatic standby.',
                                )}
                            />
                        ` : null}
                    </section>
                ` : null}

                ${sections.includes('language') ? html`
                    <section class="settings-group">
                        <h4>Language</h4>
                        <${SectionErrors} snapshot=${snapshot} section="language" actionError=${actionErrors.language} />
                        ${snapshot.language ? html`
                            <label class="settings-field">
                                <span>System language</span>
                                <select
                                    value=${String(snapshot.language.code)}
                                    disabled=${busy === 'language'}
                                    onChange=${event => mutate(
                                        'language',
                                        () => api.setLanguage(deviceId, Number(event.target.value)),
                                        'Could not update the system language.',
                                    )}
                                >
                                    ${(snapshot.language.options || []).map(option => html`
                                        <option key=${option.code} value=${String(option.code)}>${option.name}</option>
                                    `)}
                                </select>
                            </label>
                        ` : null}
                    </section>
                ` : null}

                ${sections.includes('sync') ? html`
                    <section class="settings-group">
                        <h4>Audio sync</h4>
                        <${SectionErrors} snapshot=${snapshot} section="sync" actionError=${actionErrors.sync} />
                        ${snapshot.sync ? html`
                            <fieldset class="settings-segmented" disabled=${busy === 'sync'}>
                                <legend>Playback mode</legend>
                                ${[
                                    ['SYNC_TO_ROOM', 'Video'],
                                    ['SYNC_TO_ZONE', 'Multi-room'],
                                ].map(([mode, label]) => html`
                                    <label key=${mode} class=${snapshot.sync.mode === mode ? 'selected' : ''}>
                                        <input
                                            type="radio"
                                            name=${`sync-mode-${deviceId}`}
                                            value=${mode}
                                            checked=${snapshot.sync.mode === mode}
                                            onChange=${() => mutate(
                                                'sync',
                                                () => api.setSync(deviceId, mode),
                                                'Could not update audio sync.',
                                            )}
                                        />
                                        <span>${label}</span>
                                    </label>
                                `)}
                            </fieldset>
                        ` : null}
                    </section>
                ` : null}

                ${sections.includes('bluetooth') ? html`
                    <section class="settings-group">
                        <h4>Bluetooth</h4>
                        <${SectionErrors} snapshot=${snapshot} section="bluetooth" actionError=${actionErrors.bluetooth} />
                        ${support.bluetooth && snapshot.bluetooth ? html`
                            <dl class="settings-data-list">
                                <div><dt>Status</dt><dd>${snapshot.bluetooth.connectionStatus || 'Not reported'}</dd></div>
                                ${snapshot.bluetooth.deviceName ? html`<div><dt>Device</dt><dd>${snapshot.bluetooth.deviceName}</dd></div>` : null}
                                ${snapshot.bluetooth.macAddress ? html`<div><dt>MAC address</dt><dd>${snapshot.bluetooth.macAddress}</dd></div>` : null}
                            </dl>
                        ` : null}
                        ${support.bluetoothPair ? html`
                            <button
                                class="btn-secondary settings-action"
                                disabled=${busy === 'bluetooth'}
                                onClick=${() => mutate(
                                    'bluetooth',
                                    () => api.bluetoothPair(deviceId),
                                    'Could not enter Bluetooth pairing mode.',
                                )}
                            >Enter pairing mode</button>
                        ` : null}
                        ${support.bluetoothClear ? html`
                            <div class="settings-danger-zone">
                                <button
                                    class="btn-secondary settings-action settings-danger-action"
                                    disabled=${busy === 'bluetooth'}
                                    onClick=${() => {
                                        if (!confirm('Clear all Bluetooth pairings from this speaker?')) return;
                                        mutate(
                                            'bluetooth',
                                            () => api.clearBluetoothPairings(deviceId),
                                            'Could not clear Bluetooth pairings.',
                                        );
                                    }}
                                >Clear all pairings</button>
                            </div>
                        ` : null}
                    </section>
                ` : null}

                ${sections.includes('sources') ? html`
                    <section class="settings-group">
                        <h4>Source names</h4>
                        <${SectionErrors} snapshot=${snapshot} section="sources" actionError=${actionErrors.sources} />
                        <div class="settings-source-list">
                            ${(snapshot.sources || []).map(source => html`
                                <${SourceRenameRow}
                                    key=${`${source.source}-${source.sourceAccount || ''}`}
                                    source=${source}
                                    busy=${busy === 'sources'}
                                    onRename=${(sourceName, sourceAccount, name) => mutate(
                                        'sources',
                                        () => api.setSourceName(deviceId, sourceName, sourceAccount, name),
                                        'Could not rename the source.',
                                    )}
                                />
                            `)}
                        </div>
                    </section>
                ` : null}

                ${sections.includes('network') ? html`
                    <section class="settings-group">
                        <h4>Network</h4>
                        <${SectionErrors} snapshot=${snapshot} section="network" actionError=${actionErrors.network} />
                        ${interfaces.length > 0 ? html`
                            <div class="settings-network-list">
                                ${interfaces.map((item, index) => html`
                                    <div class="settings-network-row" key=${item.macAddress || item.name || index}>
                                        <strong>${interfaceName(item)}</strong>
                                        <span>${[
                                            statusLabel(item.state),
                                            item.ipAddress,
                                            statusLabel(item.signal),
                                        ].filter(Boolean).join(' · ')}</span>
                                    </div>
                                `)}
                            </div>
                        ` : snapshot.network ? html`
                            <p class="settings-value">No network interfaces were reported.</p>
                        ` : null}
                        ${support.wifiOnboarding && snapshot.onboardingUrl ? html`
                            <a
                                class="btn-secondary settings-action settings-command-link"
                                href=${snapshot.onboardingUrl}
                                target="_blank"
                                rel="noopener"
                            >Open Wi-Fi setup</a>
                        ` : null}
                    </section>
                ` : null}
            </div>
        </details>
    `;
}
