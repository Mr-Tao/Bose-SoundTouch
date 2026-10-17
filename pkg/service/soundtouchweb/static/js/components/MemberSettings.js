import { h } from 'preact';
import { useState } from 'preact/hooks';
import htm from 'htm';
import { physicalSettingsTargets } from '../soundSettingsPresentation.mjs';
import { Settings } from './Settings.js';
import { SoundSettings } from './SoundSettings.js';

const html = htm.bind(h);

export function MemberSettings({ deviceId, device, nested = true, onOpenDetails, expanded, id, viewKey }) {
    const [open, setOpen] = useState(false);
    const targets = physicalSettingsTargets(deviceId, device);
    const showDetails = nested && Boolean(onOpenDetails);
    const controlled = nested && expanded !== undefined;
    if (!targets.length && !showDetails) return null;
    const content = html`
        ${targets.length ? html`<${SoundSettings} deviceId=${deviceId} device=${device} nested=${nested}
            active=${controlled ? expanded : open} viewKey=${viewKey} />` : null}
        ${targets.map(target => html`<${Settings} key=${`${target.controlId}:${target.physicalId}`}
            deviceId=${target.controlId} targetIdentity=${target.physicalId}
            targetName=${target.name} targetRole=${target.role} nested=${nested}
            viewKey=${viewKey ? `${viewKey}:physical:${target.physicalId}` : undefined} />`)}
        ${showDetails ? html`<button type="button" class="member-details-link"
            aria-label=${`Full device details for ${device?.info?.name || deviceId}`}
            data-detail-focus=${viewKey ? `${viewKey}:full-details` : undefined}
            onClick=${onOpenDetails}>Full device details</button>` : null}
    `;
    if (controlled) return html`<div class="member-settings" id=${id} hidden=${!expanded}>
        <div class="member-settings-content">${content}</div>
    </div>`;
    return nested ? html`<details class="member-settings settings-section"
        onToggle=${event => { if (event.target === event.currentTarget) setOpen(event.currentTarget.open); }}>
        <summary class="settings-summary"><span class="section-title">Settings · ${device?.info?.name || deviceId}</span>
            <span class="settings-chevron" aria-hidden="true"></span></summary>
        <div class="member-settings-content">${content}</div>
    </details>` : content;
}
