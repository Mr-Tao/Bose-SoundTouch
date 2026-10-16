import { h } from 'preact';
import { useState } from 'preact/hooks';
import htm from 'htm';
import { physicalSettingsTargets } from '../soundSettingsPresentation.mjs';
import { Settings } from './Settings.js';
import { SoundSettings } from './SoundSettings.js';

const html = htm.bind(h);

export function MemberSettings({ deviceId, device, nested = true, onOpenDetails }) {
    const [open, setOpen] = useState(false);
    const targets = physicalSettingsTargets(deviceId, device);
    const showDetails = nested && Boolean(onOpenDetails);
    if (!targets.length && !showDetails) return null;
    const content = html`
        ${targets.length ? html`<${SoundSettings} deviceId=${deviceId} device=${device} nested=${nested} active=${open} />` : null}
        ${targets.map(target => html`<${Settings} key=${`${target.controlId}:${target.physicalId}`}
            deviceId=${target.controlId} targetIdentity=${target.physicalId}
            targetName=${target.name} targetRole=${target.role} nested=${nested} />`)}
        ${showDetails ? html`<button type="button" class="member-details-link"
            aria-label=${`Full device details for ${device?.info?.name || deviceId}`}
            onClick=${onOpenDetails}>Full device details</button>` : null}
    `;
    return nested ? html`<details class="member-settings settings-section"
        onToggle=${event => { if (event.target === event.currentTarget) setOpen(event.currentTarget.open); }}>
        <summary class="settings-summary" aria-label=${`Settings · ${device?.info?.name || deviceId}`}><span class="section-title">Settings</span>
            <span class="settings-chevron" aria-hidden="true"></span></summary>
        <div class="member-settings-content">${content}</div>
    </details>` : content;
}
