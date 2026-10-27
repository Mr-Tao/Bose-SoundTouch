import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { api } from './api.js';
import { identityOf } from './recentsArt.mjs';

// One controller is shared by the current item, recents and the library. Reads
// coalesce, and a read begun before a write can never undo that write locally.
export function useFavorites(refreshToken = 0) {
    const [state, setState] = useState({
        entries: [], available: null, loading: true, busy: false,
        readError: null, writeError: null,
    });
    const live = useRef(false);
    const request = useRef(null);
    const pending = useRef(false);
    const writing = useRef(false);
    const generation = useRef(0);

    const read = useCallback(async function fetchFavorites() {
        if (!live.current) return;
        pending.current = true;
        if (request.current || writing.current) return;

        pending.current = false;
        const controller = new AbortController();
        const version = generation.current;
        request.current = controller;
        setState(s => ({ ...s, loading: true }));
        try {
            const response = await api.favorites({ signal: controller.signal });
            if (response?.success !== true) throw new Error(response?.error || 'Could not load favorites.');
            if (!Array.isArray(response.data?.entries)) throw new Error('Invalid favorites response.');
            if (live.current && !controller.signal.aborted && version === generation.current && !pending.current) {
                setState(s => ({ ...s, entries: response.data?.entries ?? [],
                    available: response.data?.available === true, readError: null }));
            }
        } catch (err) {
            if (live.current && !controller.signal.aborted && version === generation.current && !pending.current) {
                setState(s => ({ ...s, readError: err?.message || 'Could not load favorites.' }));
            }
        } finally {
            if (request.current === controller) {
                request.current = null;
                if (live.current) {
                    setState(s => ({ ...s, loading: false }));
                    if (pending.current && !writing.current) void fetchFavorites();
                }
            }
        }
    }, []);

    useEffect(() => {
        live.current = true;
        return () => {
            live.current = false;
            request.current?.abort();
            request.current = null;
        };
    }, []);

    useEffect(() => { void read(); }, [refreshToken, read]);

    useEffect(() => {
        const visible = () => { if (document.visibilityState === 'visible') void read(); };
        document.addEventListener('visibilitychange', visible);
        window.addEventListener('online', read);
        return () => {
            document.removeEventListener('visibilitychange', visible);
            window.removeEventListener('online', read);
        };
    }, [read]);

    const refresh = useCallback(() => {
        if (live.current) setState(s => ({ ...s, writeError: null }));
        return read();
    }, [read]);

    const mutate = useCallback(async (operation, value) => {
        if (writing.current) throw new Error('A favorite is already being saved.');
        writing.current = true;
        generation.current++;
        pending.current = true;
        if (live.current) setState(s => ({ ...s, busy: true, writeError: null }));
        try {
            const response = await (operation === 'add' ? api.addFavorite(value) : api.removeFavorite(value));
            if (response?.success !== true) throw new Error(response?.error || 'Could not update favorites.');
            if (operation === 'add' && !response.data?.id) throw new Error('The service did not return the saved favorite.');
            if (live.current) {
                setState(s => ({ ...s, available: true, entries: operation === 'add'
                    ? (s.entries.some(e => e.id === response.data.id) ? s.entries : [response.data, ...s.entries])
                    : s.entries.filter(e => e.id !== value) }));
            }
            return response.data;
        } catch (err) {
            if (live.current) setState(s => ({ ...s, writeError: err?.message || 'Could not update favorites.' }));
            throw err;
        } finally {
            writing.current = false;
            if (live.current) {
                setState(s => ({ ...s, busy: false }));
                void read();
            }
        }
    }, [read]);

    const add = useCallback(item => mutate('add', item), [mutate]);
    const remove = useCallback(id => mutate('remove', id), [mutate]);
    const has = useCallback(item => !!item?.Location && state.entries.some(
        entry => identityOf(entry.content_item) === identityOf(item)), [state.entries]);

    return { entries: state.entries, available: state.available, loading: state.loading,
        busy: state.busy, error: state.writeError || state.readError, refresh, add, remove, has };
}
