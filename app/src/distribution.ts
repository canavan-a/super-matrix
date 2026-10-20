// Which build this is (android/app/build.gradle productFlavors):
//  sideload  distributed outside Google Play (release/release.sh): an
//            always-on background connection for notifications, and the
//            in-app updater.
//  play      Google Play, which allows neither.
//  web       the browser build.
import {NativeModules, Platform} from 'react-native';

export type Distribution = 'sideload' | 'play' | 'web';

export const distribution: Distribution =
  Platform.OS === 'web' ? 'web' : NativeModules.Distribution?.flavor === 'play' ? 'play' : 'sideload';

/** The sideloaded Android build: background notifications and self-updates. */
export const isSideload = distribution === 'sideload';
