import { useEffect, useState } from 'react'
import { BrowserRouter, Routes, Route, useNavigate } from 'react-router-dom'
import { api } from './api'
import { setStreamEnabled } from './useDownloadState'
import { Login } from './pages/Login'
import { Playlists } from './pages/Playlists'
import { PlaylistDetail } from './pages/PlaylistDetail'
import { Downloads } from './pages/Downloads'
import { Search } from './pages/Search'
import { Layout } from './components/Layout'
import { Notifier } from './components/Notifier'
import { Toast, toast } from './components/Toast'
import type { User } from './types'

function AppRoutes() {
  const navigate = useNavigate()
  const [loading, setLoading] = useState(true)
  const [authed, setAuthed] = useState(false)
  const [user, setUser] = useState<User | null>(null)

  useEffect(() => {
    api.status().then((s) => {
      if (s.authenticated) {
        setAuthed(true)
        api.user().then(setUser).catch(() => {})
      }
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [])

  // The event stream needs auth; connecting while logged out only loops on 401.
  useEffect(() => {
    setStreamEnabled(authed)
  }, [authed])

  const handleLogin = async () => {
    try {
      const { url } = await api.login()
      window.location.href = url
    } catch (e) {
      toast('Failed to get login URL', 'error')
    }
  }

  const handleLogout = async () => {
    try {
      await api.logout()
      setAuthed(false)
      setUser(null)
      navigate('/')
    } catch (e) {
      toast('Logout failed', 'error')
    }
  }

  if (loading) {
    return (
      <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#727272' }}>
        <div style={{ textAlign: 'center' }}>
          <div className="spinner" />
          <p style={{ marginTop: 16 }}>Loading...</p>
        </div>
      </div>
    )
  }

  if (!authed) {
    return <Login onLogin={handleLogin} />
  }

  return (
    <Layout user={user} onLogout={handleLogout}>
      <Routes>
        <Route path="/" element={<Playlists />} />
        <Route path="/playlist/:id" element={<PlaylistDetail />} />
        <Route path="/downloads" element={<Downloads />} />
        <Route path="/search" element={<Search />} />
      </Routes>
    </Layout>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
      <Toast />
      <Notifier />
    </BrowserRouter>
  )
}
