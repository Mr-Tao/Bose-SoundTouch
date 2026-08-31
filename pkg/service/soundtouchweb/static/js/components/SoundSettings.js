import { h, htm } from '../dependencies.js';
import { bassControlForStatus } from '../bassCapabilities.mjs';
import { balanceControlState } from '../stereoBalanceResult.mjs';
import {
    deviceSoundTarget,
    stereoPairPresentation,
    targetBassStatus,
} from '../soundSettingsPresentation.mjs';
import { BassReductionControl } from './BassReductionControl.js';
import { StereoBalanceControl } from './StereoBalanceControl.js';

const html = htm.bind(h);

function soundControls(controlId, device, member) {
    const bassTarget = deviceSoundTarget(device, member);
    const bass = bassControlForStatus(targetBassStatus(bassTarget));
    const pair = stereoPairPresentation(controlId, device, member);
    const balance = pair ? balanceControlState(pair.device) : { available: false };

    return { bassTarget, bass, pair, balance };
}

export function hasSoundSettings(controlId, device = null, member = null) {
    const { bass, balance } = soundControls(controlId, device, member);
    return bass.available || balance.available;
}

export function SoundSettings({ controlId, device = null, member = null, embedded = false }) {
    const { bassTarget, bass, pair, balance } = soundControls(controlId, device, member);

    if (!bass.available && !balance.available) return null;

    const container = embedded ? 'section' : 'details';
    const className = embedded
        ? 'member-settings-group sound-settings-group'
        : 'sound-settings-section';

    return html`
        <${container} class=${className}>
            ${embedded ? html`
                <h3 class="member-settings-heading">Sound</h3>
            ` : html`<summary class="settings-summary">
                <span class="section-title">Sound settings</span>
                <span class="settings-chevron" aria-hidden="true"></span>
            </summary>`}
            <div class="sound-settings-content">
                ${bass.available ? html`<${BassReductionControl} target=${bassTarget} />` : null}
                ${balance.available ? html`
                    <${StereoBalanceControl}
                        id=${pair.controlId}
                        device=${pair.device}
                        scopeLabel=${`Stereo pair · ${pair.name}`}
                    />
                ` : null}
            </div>
        </${container}>
    `;
}
