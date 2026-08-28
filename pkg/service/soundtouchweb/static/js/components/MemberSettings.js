import { h, htm, useState } from '../dependencies.js';
import { Settings } from './Settings.js';
import { hasSoundSettings, SoundSettings } from './SoundSettings.js';

const html = htm.bind(h);

export function MemberSettings({ controlId, member, fallbackName = '' }) {
    const [open, setOpen] = useState(false);
    const deviceTarget = member?.deviceSettingsTarget;
    const soundAvailable = hasSoundSettings(controlId, null, member);
    const targetName = String(member?.name || fallbackName || '').trim();

    if (!soundAvailable && !deviceTarget?.controlId) return null;

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
                ${deviceTarget?.controlId ? html`
                    <${Settings}
                        deviceId=${deviceTarget.controlId}
                        targetName=${deviceTarget.name || fallbackName}
                        embedded=${true}
                        active=${open}
                    />
                ` : null}
            </div>
        </details>
    `;
}
