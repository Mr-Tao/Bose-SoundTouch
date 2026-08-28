import { h, htm, useState } from '../dependencies.js';
import { Settings } from './Settings.js';
import { hasSoundSettings, SoundSettings } from './SoundSettings.js';

const html = htm.bind(h);

export function MemberSettings({ controlId, member, fallbackName = '' }) {
    const [open, setOpen] = useState(false);
    const projectedTargets = Array.isArray(member?.deviceSettingsTargets)
        ? member.deviceSettingsTargets.filter(target => target?.controlId)
        : [];
    const deviceTargets = projectedTargets.length > 0
        ? projectedTargets
        : (member?.deviceSettingsTarget?.controlId ? [member.deviceSettingsTarget] : []);
    const multipleDeviceTargets = deviceTargets.length > 1;
    const soundAvailable = hasSoundSettings(controlId, null, member);
    const targetName = String(member?.name || fallbackName || '').trim();

    if (!soundAvailable && deviceTargets.length === 0) return null;

    return html`
        <details
            class="member-settings-section"
            onToggle=${event => setOpen(event.currentTarget.open)}
        >
            <summary class="settings-summary" aria-label=${`Settings for ${targetName}`}>
                <span class="section-title">Settings</span>
                <span class="settings-chevron" aria-hidden="true"></span>
            </summary>
            <div class="member-settings-content">
                ${soundAvailable ? html`
                    <${SoundSettings} controlId=${controlId} member=${member} embedded=${true} />
                ` : null}
                ${deviceTargets.map(deviceTarget => html`
                    <${Settings}
                        key=${deviceTarget.deviceId || deviceTarget.controlId}
                        deviceId=${deviceTarget.controlId}
                        targetName=${deviceTarget.name || fallbackName}
                        targetRole=${deviceTarget.role || ''}
                        embedded=${true}
                        embeddedCollapsible=${multipleDeviceTargets}
                        active=${open && !multipleDeviceTargets}
                    />
                `)}
            </div>
        </details>
    `;
}
