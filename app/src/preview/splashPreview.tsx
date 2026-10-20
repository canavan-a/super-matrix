// Dev-only entry for splash-preview.html: plays the splash for one theme, on a
// loop, without the rest of the app. ?theme=<name> picks the theme, and
// ?loop=0 plays it once and stays on the final frame.
import React, {useState} from 'react';
import ReactDOM from 'react-dom/client';

import {SplashScreen} from '../logo/SplashScreen';
import {THEMES, ThemeName} from '../theme';

const params = new URLSearchParams(window.location.search);
const name = (params.get('theme') as ThemeName) in THEMES ? (params.get('theme') as ThemeName) : 'dark';
const loop = params.get('loop') !== '0';

function Preview() {
  const [run, setRun] = useState(0);
  return <SplashScreen key={run} theme={THEMES[name]} reduceMotion={params.get('reduce') === '1'} onDone={() => loop && setRun(r => r + 1)} />;
}

ReactDOM.createRoot(document.getElementById('root')!).render(<Preview />);
