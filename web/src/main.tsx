// The search app, served at /search/.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './styles/global';
import { App } from './App';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
