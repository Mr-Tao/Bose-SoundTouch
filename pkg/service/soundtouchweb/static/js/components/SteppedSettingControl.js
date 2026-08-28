import { h, htm } from '../dependencies.js';

const html = htm.bind(h);

function steps(min, max) {
    if (!Number.isSafeInteger(min) || !Number.isSafeInteger(max) || min > max) return [];
    return Array.from({ length: max - min + 1 }, (_, index) => min + index);
}

export function SteppedSettingControl({
    label,
    scopeLabel,
    value,
    min,
    max,
    defaultValue,
    valueLabel,
    defaultLabel,
    disabled = false,
    busy = false,
    decreaseSymbol = '−',
    increaseSymbol = '+',
    decreaseLabel,
    increaseLabel,
    onDecrease,
    onIncrease,
    onReset,
    failure = '',
}) {
    const values = steps(min, max);
    const unavailable = !Number.isSafeInteger(value) || values.length === 0;
    const controlsDisabled = disabled || busy || unavailable;

    return html`
        <div class="stepped-setting" aria-busy=${busy ? 'true' : 'false'}>
            <div class="stepped-setting-heading">
                <div>
                    <div class="stepped-setting-label">${label}</div>
                    <div class="stepped-setting-scope">${scopeLabel}</div>
                </div>
                <output class="stepped-setting-value" aria-live="polite"
                    aria-label=${`${label} for ${scopeLabel}: ${unavailable ? 'Unavailable' : valueLabel}`}>
                    ${unavailable ? '–' : valueLabel}
                </output>
            </div>
            <div class="stepped-setting-controls" role="group"
                aria-label=${`${label} adjustment for ${scopeLabel}`}>
                <button type="button" class="stepped-setting-step"
                    disabled=${controlsDisabled || value <= min}
                    aria-label=${decreaseLabel} title=${decreaseLabel}
                    onClick=${onDecrease}>${decreaseSymbol}</button>
                <div class="stepped-setting-indicator" aria-hidden="true">
                    ${values.map(step => html`
                        <span key=${step} class="stepped-setting-segment
                            ${step === value ? 'current' : ''}
                            ${step === defaultValue ? 'default' : ''}"></span>
                    `)}
                </div>
                <button type="button" class="stepped-setting-step"
                    disabled=${controlsDisabled || value >= max}
                    aria-label=${increaseLabel} title=${increaseLabel}
                    onClick=${onIncrease}>${increaseSymbol}</button>
            </div>
            <div class="stepped-setting-footer">
                <span>Default ${defaultLabel}</span>
                <button type="button" class="stepped-setting-reset"
                    disabled=${controlsDisabled || value === defaultValue}
                    aria-label=${`Reset ${label.toLowerCase()} for ${scopeLabel} to ${defaultLabel}`}
                    onClick=${onReset}>Reset</button>
            </div>
            ${failure ? html`<div class="stepped-setting-failure" role="status"
                aria-label=${`${label} for ${scopeLabel}: ${failure}`}>${failure}</div>` : null}
        </div>
    `;
}
