// Draws a component at the top of the app instead of where it's written, so
// a sheet opened from inside a scrolling page or a list row covers the whole
// screen like a real modal (an absolutely positioned view otherwise only
// covers, and scrolls with, its own container). See Overlay's `modal`.
//
// The host keeps what's portalled in a small store that only PortalOutlet
// listens to, so updating it never re-renders the app around it.
import React, {createContext, useContext, useEffect, useRef, useSyncExternalStore} from 'react';
import {StyleSheet, View} from 'react-native';

interface PortalStore {
  set(id: number, node: React.ReactNode): void;
  remove(id: number): void;
  subscribe(l: () => void): () => void;
  snapshot(): [number, React.ReactNode][];
}

function createStore(): PortalStore {
  let nodes = new Map<number, React.ReactNode>();
  let snap: [number, React.ReactNode][] = [];
  const listeners = new Set<() => void>();
  const changed = () => {
    snap = [...nodes];
    listeners.forEach(l => l());
  };
  return {
    set(id, node) {
      nodes = new Map(nodes).set(id, node);
      changed();
    },
    remove(id) {
      if (!nodes.has(id)) return;
      nodes = new Map(nodes);
      nodes.delete(id);
      changed();
    },
    subscribe(l) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    snapshot: () => snap,
  };
}

const PortalContext = createContext<PortalStore | null>(null);
let nextId = 1;

export function PortalProvider({children}: {children: React.ReactNode}): React.JSX.Element {
  const store = useRef<PortalStore | null>(null);
  store.current ??= createStore();
  return <PortalContext.Provider value={store.current}>{children}</PortalContext.Provider>;
}

/** Where portalled things are drawn: last in the app's root view, over everything. */
export function PortalOutlet(): React.JSX.Element | null {
  const store = useContext(PortalContext);
  const nodes = useSyncExternalStore(
    store?.subscribe ?? (() => () => {}),
    store?.snapshot ?? (() => []),
    store?.snapshot ?? (() => []),
  );
  if (!nodes.length) return null;
  return (
    <View style={StyleSheet.absoluteFill} pointerEvents="box-none">
      {nodes.map(([id, node]) => (
        <React.Fragment key={id}>{node}</React.Fragment>
      ))}
    </View>
  );
}

/** Renders children in the PortalOutlet (or in place, if there's no provider). */
export function Portal({children}: {children: React.ReactNode}): React.JSX.Element | null {
  const store = useContext(PortalContext);
  const id = useRef(0);
  if (!id.current) id.current = nextId++;
  useEffect(() => {
    store?.set(id.current, children);
  });
  useEffect(() => {
    const mine = id.current;
    return () => store?.remove(mine);
  }, [store]);
  return store ? null : <>{children}</>;
}
