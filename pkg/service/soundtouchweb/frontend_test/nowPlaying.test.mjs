import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const nowPlaying = await readFile(
    new URL('../static/js/components/NowPlaying.js', import.meta.url), 'utf8');
const presetPicker = await readFile(
    new URL('../static/js/components/PresetPicker.js', import.meta.url), 'utf8');

// Radio stations carry their logo on the ContentItem rather than in the <art>
// element, so the now-playing card showed nothing for a TuneIn station whose
// preset tile displayed the same logo fine. The Go model already resolves both
// in this order (NowPlaying.GetArtworkURL); the frontend has to match it.
test('the now-playing card falls back to the ContentItem artwork', () => {
    assert.match(nowPlaying, /nowPlaying\.Art\?\.URL \|\| nowPlaying\.ContentItem\?\.ContainerArt/);
});

test('the explicit preset action names its current slot when choosing a destination', () => {
    assert.match(presetPicker, /Already in preset \$\{mappedSlot\}/);
    assert.match(presetPicker, /label = 'Save as preset'/);
});

// The now-playing card keeps its own star styling and slot mapping, and hands
// them to the shared picker rather than rendering a second popover.
test('the now-playing card delegates preset saving to the shared picker', () => {
    assert.match(nowPlaying, /import \{ PresetPicker \} from '\.\/PresetPicker\.js'/);
    assert.match(nowPlaying, /mappedSlot=\$\{mappedPreset \? mappedPreset\.ID : null\}/);
});
