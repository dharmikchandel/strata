// The landing page, served at /.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../styles/global';
import { Landing } from './Landing';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Landing />
  </StrictMode>,
);
