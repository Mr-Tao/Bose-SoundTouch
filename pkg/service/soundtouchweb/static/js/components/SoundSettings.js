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

export function SoundSettings({ controlId, device = null, member = null }) {
    const bassTarget = deviceSoundTarget(device, member);
    const bass = bassControlForStatus(targetBassStatus(bassTarget));
    const pair = stereoPairPresentation(controlId, device, member);
    const balance = pair ? balanceControlState(pair.device) : { available: false };

    if (!bass.available && !balance.available) return null;

    return html`
        <details class="sound-settings-section">
            <summary class="settings-summary">
                <span class="section-title">Sound settings</span>
                <span class="settings-chevron" aria-hidden="true"></span>
            </summary>
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
        </details>
    `;
}
