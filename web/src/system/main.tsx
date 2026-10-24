// The system view, served at /system/.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../styles/global';
import { SystemApp } from './SystemApp';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <SystemApp />
  </StrictMode>,
);
