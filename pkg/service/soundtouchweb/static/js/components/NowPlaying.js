import { h } from 'preact';
import { useState, useEffect } from 'preact/hooks';
import htm from 'htm';
import { SourceIcon } from '../sourceIcons.js';
import { FavoriteButton } from './FavoriteButton.js';
import { PresetPicker } from './PresetPicker.js';
import { sameContentLocation } from '../contentLocation.mjs';
import { currentPresetContent } from '../presetMutations.mjs';

const html = htm.bind(h);

function fmt(secs) {
    if (!secs || secs <= 0) return '0:00';
    const m = Math.floor(secs / 60);
    const s = secs % 60;
    return `${m}:${s.toString().padStart(2, '0')}`;
}

// The picker chooses a destination; the shared controller freezes this
// ContentItem and confirms any move/overwrite against the speaker's bank.
function NowPlayingPresetPicker({ nowPlaying, presets, presetMutations }) {
    const current = currentPresetContent(nowPlaying);
    // Detect whether the current content is already saved as any preset.
    const currentSource = nowPlaying?.ContentItem?.Source;
    const currentLocation = nowPlaying?.ContentItem?.Location;
    const presetList = presets?.Preset ?? [];
    // Keep the matching preset, not just a boolean, so the tooltip can name
    // the slot it landed in.
    const mappedPreset = currentLocation
        ? presetList.find(p =>
            p.ContentItem?.Source === currentSource &&
            sameContentLocation(currentSource, p.ContentItem?.Location, currentLocation))
        : undefined;

    function save(slotId) {
        if (current) presetMutations?.request(slotId, current);
    }

    return html`<${PresetPicker}
        onSave=${save}
        disabled=${!current || !presetMutations}
        mappedSlot=${mappedPreset ? mappedPreset.ID : null}
        wrapClass="now-playing-fav-wrap"
        buttonClass="now-playing-fav-btn"
        overlayClass="now-playing-fav-overlay"
    />`;
}

export function NowPlaying({ nowPlaying, deviceId, presets, favorites, presetMutations }) {
    const [position, setPosition] = useState(0);

    useEffect(() => {
        const pos = nowPlaying?.Time?.Position ?? 0;
        setPosition(pos);
        if (nowPlaying?.PlayStatus !== 'PLAY_STATE') return;
        const id = setInterval(() => setPosition(p => p + 1), 1000);
        return () => clearInterval(id);
    }, [nowPlaying?.Time?.Position, nowPlaying?.PlayStatus,
        nowPlaying?.TrackID, nowPlaying?.ContentItem?.Location]);

    if (!nowPlaying || nowPlaying.Source === 'STANDBY') {
        return html`<div class="now-playing standby">Standby</div>`;
    }

    const title = nowPlaying.Track || nowPlaying.StationName || nowPlaying.Source;
    const longMetadata = [title, nowPlaying.Artist, nowPlaying.Album]
        .some(value => value && value.length > 80);
    const isRAOP = nowPlaying.Source === 'AIRPLAY' || nowPlaying.Source === 'RAOP';
    const showFullMetadata = isRAOP || longMetadata;
    // Same precedence as the model's GetArtworkURL (nowplaying.go): the <art>
    // element first, then the ContentItem's own artwork, which is where radio
    // stations carry their logo.
    const artURL = nowPlaying.Art?.URL || nowPlaying.ContentItem?.ContainerArt;
    const isBuffering = nowPlaying.PlayStatus === 'BUFFERING_STATE';
    const total = nowPlaying.Time?.Total ?? 0;
    const pct = total > 0 ? Math.min(100, (position / total) * 100) : 0;

    return html`
        <div class="now-playing">
            ${artURL
                ? html`<img class="album-art" src=${artURL} alt="" />`
                : html`<div class="album-art album-art-empty" data-source=${nowPlaying.Source ?? ''}>
                    <${SourceIcon} source=${nowPlaying.Source} className="now-playing-source-icon" />
                </div>`
            }
            <div class="track-info">
                <div class="now-playing-label">Now playing</div>
                <div class="track-title" title=${title}>${title}</div>
                ${nowPlaying.Artist && html`<div class="track-artist" title=${nowPlaying.Artist}>${nowPlaying.Artist}</div>`}
                ${nowPlaying.Album && html`<div class="track-album" title=${nowPlaying.Album}>${nowPlaying.Album}</div>`}
                <div class="track-meta">
                    <span class="track-source">${nowPlaying.Source}</span>
                    ${isBuffering && html`<span class="buffering-badge">Buffering…</span>`}
                </div>
                <${FavoriteButton} item=${nowPlaying.ContentItem} favorites=${favorites} />
                ${showFullMetadata && html`
                    <details class="track-details">
                        <summary>Full details</summary>
                        <dl>
                            <dt>Title</dt><dd>${title}</dd>
                            ${nowPlaying.Artist && html`<dt>Artist</dt><dd>${nowPlaying.Artist}</dd>`}
                            ${nowPlaying.Album && html`<dt>Album</dt><dd>${nowPlaying.Album}</dd>`}
                        </dl>
                    </details>
                `}
                ${total > 0 && html`
                    <div class="progress-row">
                        <div class="progress-bar">
                            <div class="progress-fill" style="width:${pct}%"></div>
                        </div>
                        <span class="progress-time">${fmt(Math.min(position, total))} / ${fmt(total)}</span>
                    </div>
                `}
            </div>
            ${deviceId && html`
                <${NowPlayingPresetPicker}
                    deviceId=${deviceId}
                    nowPlaying=${nowPlaying}
                    presets=${presets}
                    presetMutations=${presetMutations}
                />
            `}
        </div>
    `;
}
