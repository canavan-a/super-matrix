import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './src/App';

const root = document.getElementById('root');
if (!root) {
  throw new Error('missing #root element');
}

ReactDOM.createRoot(root).render(<App />);
